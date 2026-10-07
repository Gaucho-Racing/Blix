import hashlib
import hmac
import io
import json
import logging
import os
import re
import secrets
import socket
import tarfile
import tempfile
import threading
import time
from dataclasses import dataclass, field
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import httpx
from pydantic import ValidationError

from blix_sandbox_worker.contracts import CodingOutput, SandboxAnswer, SandboxQuestion, SandboxRequest, SandboxTraceEvent
from blix_sandbox_worker.github_auth import github_app_configured, github_credentials, github_token

logger = logging.getLogger(__name__)
TASK_ID = re.compile(r"^[a-z][a-z0-9_]{1,80}$")
LABEL = "blix.sandbox"


class SandboxError(Exception):
    pass


@dataclass
class Task:
    request: SandboxRequest
    credential_secret: str = field(default_factory=lambda: secrets.token_urlsafe(32))
    status: str = "queued"
    result: dict | None = None
    error: str | None = None
    cancelled: threading.Event = field(default_factory=threading.Event)
    updated_at: float = field(default_factory=time.monotonic)
    question: SandboxQuestion | None = None
    trace: list[SandboxTraceEvent] = field(default_factory=list)
    answers: dict[str, SandboxAnswer] = field(default_factory=dict)
    input_lock: threading.Lock = field(default_factory=threading.Lock)


class SandboxHost:
    def __init__(self) -> None:
        self.token = os.environ.get("BLIX_INTERNAL_TOKEN", "")
        if not self.token:
            raise ValueError("BLIX_INTERNAL_TOKEN is required.")
        self.image = os.environ.get("SANDBOX_IMAGE", "blix-ohmypi:18.5.0")
        self.repositories = {
            value.strip().lower()
            for value in os.environ.get("SANDBOX_REPOSITORIES", "gaucho-racing/*").split(",")
            if value.strip()
        }
        self.timeout = int(os.environ.get("SANDBOX_TIMEOUT_SECONDS", "1800"))
        self.tasks: dict[str, Task] = {}
        self.lock = threading.Lock()
        self.credential_lock = threading.Lock()
        self.credential_cache: dict[bool, dict] = {}
        self.slots = threading.BoundedSemaphore(int(os.environ.get("SANDBOX_CONCURRENCY", "2")))
        self.docker = httpx.Client(
            base_url="http://docker",
            timeout=60,
            transport=httpx.HTTPTransport(uds=os.environ.get("DOCKER_SOCKET", "/docker/docker.sock")),
        )

    def engine(self, method: str, path: str, **kwargs) -> httpx.Response:
        response = self.docker.request(method, path, **kwargs)
        if response.is_error:
            raise SandboxError(f"Docker {method} {path} failed (HTTP {response.status_code}).")
        return response

    @staticmethod
    def names(task_id: str) -> tuple[str, str]:
        digest = hashlib.sha256(task_id.encode()).hexdigest()[:24]
        return f"blix-task-{digest}", f"blix-work-{digest}"

    def submit(self, task_id: str, request: SandboxRequest) -> Task:
        if request.requester_github_id is not None and not (
            os.environ.get("SANDBOX_GITHUB_TOKEN") or github_app_configured()
        ):
            raise ValueError("GitHub write tasks require GitHub credentials.")
        if request.requester_github_id is not None:
            if not request.requester_github_id or not request.requester_name:
                raise ValueError("GitHub write tasks require a verified linked GitHub account and requester name.")
            if not request.requester_email:
                raise ValueError("GitHub write tasks require the requester's verified Sentinel email.")
            response = httpx.get(
                f"https://api.github.com/user/{request.requester_github_id}",
                headers={"Authorization": f"Bearer {github_token()}", "Accept": "application/vnd.github+json"},
                timeout=30,
            )
            if response.is_error:
                raise ValueError("Could not resolve the requester's linked GitHub account.")
            account = response.json()
            if account.get("id") != request.requester_github_id:
                raise ValueError("Linked GitHub account identity did not match.")
            request = SandboxRequest.model_validate(
                {
                    **request.model_dump(),
                    "requester_github_login": account.get("login"),
                }
            )
        with self.lock:
            existing = self.tasks.get(task_id)
            if existing:
                if existing.request != request:
                    raise ValueError("Task ID already belongs to a different request.")
                return existing
            self.reap_results()
            if len(self.tasks) >= 100:
                raise ValueError("Sandbox host task capacity reached.")
            task = Task(request=request)
            self.tasks[task_id] = task
        threading.Thread(target=self.execute, args=(task_id, task), daemon=True).start()
        return task

    def reap_results(self) -> None:
        for task_id, task in list(self.tasks.items()):
            if task.status in {"succeeded", "failed", "cancelled"} and time.monotonic() - task.updated_at > 3600:
                del self.tasks[task_id]

    def remove(self, task_id: str) -> None:
        container, volume = self.names(task_id)
        for path in (f"/containers/{container}?force=true&v=true", f"/volumes/{volume}"):
            response = self.docker.delete(path)
            if response.status_code not in {204, 404}:
                raise SandboxError(f"Sandbox cleanup failed (HTTP {response.status_code}).")

    def recover(self) -> None:
        filters = json.dumps({"label": [f"{LABEL}=true"]})
        containers = self.engine("GET", "/containers/json", params={"all": "true", "filters": filters}).json()
        for container in containers:
            self.engine("DELETE", f"/containers/{container['Id']}?force=true&v=true")
        volumes = self.engine("GET", "/volumes", params={"filters": filters}).json().get("Volumes") or []
        for volume in volumes:
            self.engine("DELETE", f"/volumes/{volume['Name']}")

    def ensure_image(self) -> None:
        response = self.docker.get(f"/images/{self.image}/json")
        if response.status_code == 404:
            with self.docker.stream("POST", "/images/create", params={"fromImage": self.image}, timeout=600) as pull:
                pull.raise_for_status()
                for line in pull.iter_lines():
                    if line and "error" in json.loads(line):
                        raise SandboxError("Sandbox image pull failed; check SANDBOX_IMAGE and registry access.")
        elif response.is_error:
            raise SandboxError("Could not inspect the sandbox image.")

    def upload(self, container: str, repo: Path, request: SandboxRequest) -> None:
        with tempfile.TemporaryFile() as archive:
            with tarfile.open(fileobj=archive, mode="w") as tar:

                def ownership(info: tarfile.TarInfo) -> tarfile.TarInfo:
                    info.uid = info.gid = 1000
                    info.uname = info.gname = "sandbox"
                    return info

                tar.add(repo, arcname="repo", filter=ownership)
                data = json.dumps(
                    {**request.model_dump(mode="json"), "repository_access": sorted(self.repositories)}
                ).encode()
                info = tarfile.TarInfo("task.json")
                info.size, info.uid, info.gid = len(data), 1000, 1000
                tar.addfile(info, io.BytesIO(data))
            if archive.tell() > 256 * 1024 * 1024:
                raise SandboxError("Repository exceeds the MVP's 256 MB archive limit.")
            archive.seek(0)
            self.engine(
                "PUT",
                f"/containers/{container}/archive",
                params={"path": "/workspace"},
                content=iter(lambda: archive.read(65536), b""),
                headers={"Content-Type": "application/x-tar"},
            )

    def read_json(self, container: str, path: str, *, optional: bool = False) -> dict | None:
        with self.docker.stream(
            "GET",
            f"/containers/{container}/archive",
            params={"path": path},
        ) as response:
            if optional and response.status_code == 404:
                return None
            if response.is_error:
                raise SandboxError("Sandbox exited without a result file.")
            data = bytearray()
            for chunk in response.iter_bytes():
                data.extend(chunk)
                if len(data) > 1024 * 1024:
                    raise SandboxError("Sandbox result exceeds 1 MB.")
        with tarfile.open(fileobj=io.BytesIO(data)) as archive:
            member = archive.next()
            if member is None or not member.isfile() or member.size > 500000:
                raise SandboxError("Invalid sandbox result archive.")
            file = archive.extractfile(member)
            if file is None:
                raise SandboxError("Missing sandbox result.")
            payload = json.load(file)
        if not isinstance(payload, dict):
            raise SandboxError("Invalid sandbox JSON payload.")
        return payload

    def result(self, container: str) -> CodingOutput:
        payload = self.read_json(container, "/results/result.json")
        if "error" in payload:
            raise SandboxError(str(payload["error"])[:8000])
        return CodingOutput.model_validate(payload)

    def refresh_trace(self, container: str, task: Task) -> None:
        try:
            payload = self.read_json(container, "/results/trace.json", optional=True)
            if payload is None:
                return
            events = [SandboxTraceEvent.model_validate(event) for event in payload["events"]]
            if len(events) > 256 or any(left.sequence >= right.sequence for left, right in zip(events, events[1:])):
                raise ValueError("Invalid sandbox trace sequence.")
            with task.input_lock:
                task.trace = events
        except (SandboxError, ValueError, KeyError, TypeError, httpx.HTTPError):
            logger.warning("Could not collect sandbox trace for %s", container, exc_info=True)

    def refresh_question(self, container: str, task: Task) -> None:
        payload = self.read_json(container, "/results/question.json", optional=True)
        if payload is None:
            return
        question = SandboxQuestion.model_validate(payload)
        with task.input_lock:
            if question.id in task.answers:
                return
            if task.question is not None and task.question != question:
                raise SandboxError("Sandbox changed its question before receiving an answer.")
            task.question = question
            task.status = "waiting_for_input"

    def answer(self, task_id: str, answer: SandboxAnswer) -> None:
        with self.lock:
            task = self.tasks.get(task_id)
        if task is None:
            raise ValueError("Sandbox task not found.")
        with task.input_lock:
            previous = task.answers.get(answer.question_id)
            if previous is not None:
                if previous != answer:
                    raise ValueError("Question already has a different answer.")
                return
            if task.cancelled.is_set() or task.status != "waiting_for_input" or task.question is None:
                raise ValueError("Sandbox task is not waiting for input.")
            answer.validate_question(task.question)
            data = answer.model_dump_json().encode()
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode="w") as tar:
                info = tarfile.TarInfo("answer.json")
                info.size, info.uid, info.gid = len(data), 1000, 1000
                tar.addfile(info, io.BytesIO(data))
            container, _ = self.names(task_id)
            self.engine(
                "PUT",
                f"/containers/{container}/archive",
                params={"path": "/results"},
                content=archive.getvalue(),
                headers={"Content-Type": "application/x-tar"},
            )
            task.answers[answer.question_id] = answer
            task.question = None
            task.status = "running"

    def credentials(self, task_id: str, authorization: str) -> dict:
        with self.lock:
            task = self.tasks.get(task_id)
        if task is None or not hmac.compare_digest(authorization, "Bearer " + task.credential_secret):
            raise PermissionError("Invalid sandbox credential grant.")
        with self.credential_lock:
            if task.cancelled.is_set() or task.status not in {"running", "waiting_for_input"}:
                raise PermissionError("Sandbox credential grant is inactive.")
            write = task.request.requester_github_id is not None
            cached = self.credential_cache.get(write)
            if cached is not None and cached.get("expires_at"):
                expires = datetime.fromisoformat(cached["expires_at"].replace("Z", "+00:00"))
                if (expires - datetime.now(UTC)).total_seconds() > 300:
                    return cached
            elif cached is not None:
                return cached
            if not write and not github_app_configured():
                raise PermissionError("Read-only GitHub access requires a GitHub App token.")
            self.credential_cache[write] = github_credentials(write=write)
            return self.credential_cache[write]

    def execute(self, task_id: str, task: Task) -> None:
        container, volume = self.names(task_id)
        try:
            with self.slots:
                if task.cancelled.is_set():
                    return
                task.status = "running"
                model = os.environ.get("SANDBOX_MODEL", "gpt-6-astra")
                if not model:
                    raise SandboxError("SANDBOX_MODEL is required.")
                with tempfile.TemporaryDirectory(prefix="blix-sandbox-worker-") as directory:
                    root = Path(directory)
                    repo = root / "repo"
                    repo.mkdir()
                    self.engine("POST", "/volumes/create", json={"Name": volume, "Labels": {LABEL: "true"}})
                    credential_host = socket.gethostbyname(socket.gethostname())
                    credential_port = os.environ.get("SANDBOX_PORT", "8080")
                    environment = [
                        f"SANDBOX_MODEL={model}",
                        f"SANDBOX_TIMEOUT_SECONDS={self.timeout}",
                        f"SANDBOX_CREDENTIAL_URL=http://{credential_host}:{credential_port}/tasks/{task_id}/credentials",
                        f"SANDBOX_CREDENTIAL_SECRET={task.credential_secret}",
                    ]
                    for key in ("LLM_GATEWAY_ENDPOINT", "LLM_GATEWAY_API_KEY"):
                        if os.environ.get(key):
                            environment.append(f"{key}={os.environ[key]}")
                    self.engine(
                        "POST",
                        "/containers/create",
                        params={"name": container},
                        json={
                            "Image": self.image,
                            "User": "1000:1000",
                            "Env": environment,
                            "Labels": {LABEL: "true"},
                            "WorkingDir": "/workspace",
                            "HostConfig": {
                                "Mounts": [{"Type": "volume", "Source": volume, "Target": "/workspace"}],
                                "Memory": int(os.environ.get("SANDBOX_MEMORY_MB", "2048")) * 1024 * 1024,
                                "NanoCpus": int(float(os.environ.get("SANDBOX_CPUS", "2")) * 1e9),
                                "PidsLimit": 256,
                                "CapDrop": ["ALL"],
                                "SecurityOpt": ["no-new-privileges:true"],
                                "LogConfig": {"Type": "json-file", "Config": {"max-size": "5m", "max-file": "1"}},
                            },
                        },
                    )
                    self.upload(container, repo, task.request)
                    if task.cancelled.is_set():
                        return
                    self.engine("POST", f"/containers/{container}/start")
                    deadline = time.monotonic() + self.timeout + 60
                    while self.engine("GET", f"/containers/{container}/json").json()["State"]["Running"]:
                        self.refresh_trace(container, task)
                        self.refresh_question(container, task)
                        if task.cancelled.wait(1):
                            return
                        if time.monotonic() > deadline:
                            raise SandboxError("Sandbox execution timed out.")
                    self.refresh_trace(container, task)
                    result = self.result(container)
                    if task.cancelled.is_set():
                        return
                    task.result = result.model_dump(mode="json")
        except Exception as error:
            logger.exception("Sandbox task %s failed", task_id)
            task.error = str(error)[:8000]
        finally:
            try:
                self.remove(task_id)
            except Exception:
                logger.exception("Sandbox cleanup failed for %s", task_id)
                task.error = "Sandbox cleanup failed; operator intervention is required."
            task.status = "cancelled" if task.cancelled.is_set() else "failed" if task.error else "succeeded"
            task.updated_at = time.monotonic()


class Handler(BaseHTTPRequestHandler):
    host: SandboxHost

    def respond(self, status: int, body: dict) -> None:
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def dispatch(self) -> None:
        parts = self.path.split("/")
        if len(parts) == 4 and parts[1] == "tasks" and parts[3] == "credentials" and self.command == "GET":
            if not TASK_ID.fullmatch(parts[2]):
                self.respond(404, {"error": "Not found"})
                return
            try:
                credentials = self.host.credentials(parts[2], self.headers.get("Authorization", ""))
            except PermissionError:
                self.respond(403, {"error": "Credential grant unavailable"})
                return
            self.respond(200, credentials)
            return
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + self.host.token):
            self.respond(401, {"error": "Unauthorized"})
            return
        if self.path == "/health" and self.command == "GET":
            self.host.engine("GET", "/_ping")
            self.respond(200, {"healthy": True})
            return
        parts = self.path.split("/")
        is_answer = len(parts) == 4 and parts[3] == "answers" and self.command == "POST"
        if (len(parts) != 3 and not is_answer) or parts[1] != "tasks" or not TASK_ID.fullmatch(parts[2]):
            self.respond(404, {"error": "Not found"})
            return
        task_id = parts[2]
        if self.command == "POST":
            length = int(self.headers.get("Content-Length", "0"))
            limit = 200000 if is_answer else 600000
            if not 0 < length <= limit:
                raise ValueError(f"Request must be between 1 and {limit} bytes.")
            data = self.rfile.read(length)
            if is_answer:
                self.host.answer(task_id, SandboxAnswer.model_validate_json(data))
                self.respond(200, {"accepted": True})
                return
            task = self.host.submit(task_id, SandboxRequest.model_validate_json(data))
        else:
            with self.host.lock:
                task = self.host.tasks.get(task_id)
            if task is None:
                self.respond(404, {"error": "Task not found; ephemeral host state may have been lost."})
                return
            if self.command == "DELETE":
                if task.status not in {"succeeded", "failed", "cancelled"}:
                    task.cancelled.set()
        with task.input_lock:
            payload = {
                "status": task.status,
                "result": task.result,
                "error": task.error,
                "question": task.question.model_dump(mode="json") if task.question else None,
                "trace": [event.model_dump(mode="json") for event in task.trace],
            }
        self.respond(202 if self.command == "POST" else 200, payload)

    def handle_request(self) -> None:
        self.connection.settimeout(30)
        try:
            self.dispatch()
        except (ValueError, ValidationError) as error:
            self.respond(400, {"error": str(error)[:2000]})
        except Exception:
            logger.exception("Sandbox API request failed")
            self.respond(503, {"error": "Sandbox host unavailable"})

    do_GET = handle_request
    do_POST = handle_request
    do_DELETE = handle_request


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    host = SandboxHost()
    host.recover()
    host.ensure_image()
    Handler.host = host
    ThreadingHTTPServer(("0.0.0.0", int(os.environ.get("SANDBOX_PORT", "8080"))), Handler).serve_forever()


if __name__ == "__main__":
    main()
