import json
import logging
import os
import queue
import re
import subprocess
import threading
import time
from collections import deque
from datetime import UTC, datetime
from pathlib import Path


class TraceRecorder:
    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.events: deque[dict] = deque()
        self.sequence = 0
        self.size = 0
        self.lock = threading.Lock()
        self.secrets = [
            value
            for key, value in os.environ.items()
            if any(word in key.upper() for word in ("TOKEN", "SECRET", "PASSWORD", "API_KEY")) and len(value) >= 8
        ]

    def record(
        self, kind: str, content: str = "", *, tool_name: str = "", tool_call_id: str = "", is_error: bool = False
    ) -> None:
        for secret in self.secrets:
            content = content.replace(secret, "[REDACTED]")
        content = re.sub(r"(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]+", r"\1[REDACTED]", content)
        content = re.sub(
            r"(?i)((?:api[_-]?key|token|password|secret)[\"']?\s*[:=]\s*[\"']?)[^\s\"',}]+", r"\1[REDACTED]", content
        )
        content = re.sub(r"\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)\b", "[REDACTED]", content)
        encoded = content.encode()
        truncated = len(encoded) > 6000
        content = (
            (
                encoded[:3000].decode(errors="ignore")
                + "\n[output truncated]\n"
                + encoded[-3000:].decode(errors="ignore")
            )
            if truncated
            else content
        )
        with self.lock:
            self.sequence += 1
            event = {
                "sequence": self.sequence,
                "timestamp": datetime.now(UTC).isoformat(),
                "kind": kind[:100],
                "content": content,
                "tool_name": tool_name[:200],
                "tool_call_id": tool_call_id[:200],
                "is_error": is_error,
                "truncated": truncated,
            }
            size = len(json.dumps(event).encode())
            self.events.append(event)
            self.size += size
            while len(self.events) > 256 or self.size > 380000:
                self.size -= len(json.dumps(self.events.popleft()).encode())
            path = self.directory / "trace.json"
            try:
                temporary = path.with_suffix(".tmp")
                temporary.write_text(json.dumps({"events": list(self.events)}))
                temporary.replace(path)
            except OSError as error:
                logging.getLogger(__name__).warning("Could not write blix_code_v1 trace snapshot: %s", error)

    def capture(self, event: dict) -> None:
        kind = event.get("type")
        if kind in {"tool_execution_start", "tool_execution_update", "tool_execution_end"}:
            value = (
                event.get("args")
                if kind == "tool_execution_start"
                else event.get("result", event.get("partialResult", {}))
            )
            content = json.dumps(value, indent=2)
            if kind != "tool_execution_start" and isinstance(value, dict) and isinstance(value.get("content"), list):
                content = "\n".join(item.get("text", "") for item in value["content"] if item.get("type") == "text")
            self.record(
                kind,
                content,
                tool_name=event.get("toolName", ""),
                tool_call_id=event.get("toolCallId", ""),
                is_error=bool(event.get("isError")),
            )
        elif kind == "message_end" and event.get("message", {}).get("role") == "assistant":
            text = "\n".join(
                item.get("text", "") for item in event["message"].get("content", []) if item.get("type") == "text"
            )
            if text:
                self.record("assistant_message", text)
        elif kind in {"agent_start", "agent_end", "turn_start", "session_settled"}:
            self.record(kind)
        elif kind in {"auto_retry_start", "auto_retry_end", "extension_error", "notice", "prompt_result"}:
            self.record(
                kind,
                json.dumps(
                    {
                        key: value
                        for key, value in event.items()
                        if key in {"status", "error", "message", "attempt", "maxAttempts", "delayMs"}
                    }
                ),
                is_error=bool(event.get("error")),
            )


class OmpSession:
    def __init__(
        self, command: list[str], directory: Path, timeout: int, repository: Path = Path("/workspace/repo")
    ) -> None:
        self.command = command
        self.repository = repository
        self.directory = directory
        self.timeout = timeout
        self.question: dict | None = None
        self.events: queue.Queue = queue.Queue(maxsize=128)
        self.stopping = threading.Event()
        self.errors: deque[str] = deque(maxlen=20)
        self.trace = TraceRecorder(directory)

    def enqueue(self, event) -> None:
        while not self.stopping.is_set():
            try:
                self.events.put(event, timeout=0.2)
                return
            except queue.Full:
                continue

    def read_events(self, stream) -> None:
        try:
            while line := stream.readline(1024 * 1024 + 1):
                if len(line) > 1024 * 1024:
                    raise RuntimeError("blix_code_v1 RPC frame exceeds 1 MB.")
                if line.strip():
                    event = json.loads(line)
                    if not isinstance(event, dict):
                        raise RuntimeError("Invalid blix_code_v1 RPC event.")
                    self.enqueue(event)
        except Exception as error:
            self.enqueue(error)
        finally:
            self.enqueue(None)

    def read_errors(self, stream) -> None:
        while line := stream.readline(8192):
            self.errors.append(line[-8192:])
            self.trace.record("stderr", line[-8192:], is_error=True)

    def send(self, message: dict) -> None:
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def receive_answer(self) -> None:
        path = self.directory / "answer.json"
        if self.question is None or not path.is_file():
            return
        try:
            answer = json.loads(path.read_text())
        except (json.JSONDecodeError, FileNotFoundError):
            return
        if answer["question_id"] != self.question["id"]:
            raise RuntimeError("Answer belongs to a different blix_code_v1 question.")
        self.send(
            {
                "type": "extension_ui_response",
                "id": self.question["id"],
                "answers": [
                    {"id": item["id"], "selectedOptions": item["selected_options"], "customInput": item["text"]}
                    for item in answer["answers"]
                ],
            }
        )
        self.trace.record("answer_delivered", "Clarification answer sent to blix_code_v1.")
        path.unlink()
        (self.directory / "question.json").unlink(missing_ok=True)
        self.question = None

    def handle_question(self, event: dict) -> None:
        method = event.get("method")
        if method == "cancel":
            if self.question is not None and event.get("id") == self.question["id"]:
                raise RuntimeError("blix_code_v1 cancelled its pending clarification request.")
            return
        if method in {"notify", "setStatus", "setWidget", "setTitle", "set_editor_text"}:
            return
        if method != "ask" or self.question is not None:
            raise RuntimeError(f"Unsupported blix_code_v1 UI request: {method}.")
        self.question = {"id": event["id"], "questions": event["questions"]}
        self.trace.record("question", json.dumps(self.question))
        path = self.directory / "question.json"
        temporary = path.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.question))
        temporary.replace(path)

    def run(self, prompt: str) -> str:
        deadline = time.monotonic() + self.timeout
        self.process = subprocess.Popen(
            self.command,
            cwd=self.repository,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )
        readers = [
            threading.Thread(target=self.read_events, args=(self.process.stdout,), daemon=True),
            threading.Thread(target=self.read_errors, args=(self.process.stderr,), daemon=True),
        ]
        for reader in readers:
            reader.start()
        prompt_finished = False
        summary_requested = False
        try:
            while time.monotonic() < deadline:
                self.receive_answer()
                try:
                    event = self.events.get(timeout=0.2)
                except queue.Empty:
                    continue
                if event is None:
                    raise RuntimeError("blix_code_v1 RPC exited unexpectedly: " + "".join(self.errors)[-4000:])
                if isinstance(event, Exception):
                    raise event
                self.trace.capture(event)
                kind = event.get("type")
                if kind == "ready":
                    self.send({"id": "ask-config", "type": "set_ask_dialog", "enabled": True})
                elif kind == "extension_ui_request":
                    self.handle_question(event)
                elif kind == "response":
                    if not event.get("success"):
                        raise RuntimeError(
                            "blix_code_v1 RPC failed: " + str(event.get("error", "Unknown error"))[:4000]
                        )
                    if event.get("id") == "ask-config":
                        self.send({"id": "task-prompt", "type": "prompt", "message": prompt})
                    elif event.get("id") == "task-summary":
                        return str(
                            (event.get("data") or {}).get("text") or "Task completed without an assistant summary."
                        )[-64000:]
                elif kind == "prompt_result" and event.get("id") == "task-prompt":
                    if event.get("status") != "completed":
                        raise RuntimeError(
                            "blix_code_v1 task failed: " + str(event.get("error") or event.get("status"))
                        )
                    prompt_finished = True
                    if event.get("sessionSettled"):
                        self.send({"id": "task-summary", "type": "get_last_assistant_text"})
                        summary_requested = True
                elif kind == "session_settled" and prompt_finished and not summary_requested:
                    self.send({"id": "task-summary", "type": "get_last_assistant_text"})
                    summary_requested = True
            raise TimeoutError("blix_code_v1 task deadline exceeded, including time waiting for clarification.")
        finally:
            self.stopping.set()
            (self.directory / "question.json").unlink(missing_ok=True)
            self.process.stdin.close()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
            for reader in readers:
                reader.join()
            self.process.stdout.close()
            self.process.stderr.close()
