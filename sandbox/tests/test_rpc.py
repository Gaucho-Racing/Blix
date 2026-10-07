import importlib.util
import json
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("sandbox_rpc", Path(__file__).parents[1] / "images/ohmypi/rpc.py")
rpc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rpc)

FAKE_OMP = """
import json, sys

def emit(data):
    print(json.dumps(data), flush=True)

def read():
    return json.loads(sys.stdin.readline())

emit({"type": "ready"})
assert read() == {"id": "ask-config", "type": "set_ask_dialog", "enabled": True}
emit({"type": "response", "id": "ask-config", "success": True})
assert read()["type"] == "prompt"
emit({"type": "response", "id": "task-prompt", "success": True})
emit({"type": "extension_ui_request", "id": "ui_1", "method": "ask", "questions": [
    {"id": "scope", "question": "Which scope?", "options": [{"label": "Minimal"}, {"label": "Broad"}]},
    {"id": "context", "question": "Any constraints?", "options": []}
]})
answer = read()
assert answer["type"] == "extension_ui_response"
assert answer["id"] == "ui_1"
assert answer["answers"] == [
    {"id": "scope", "selectedOptions": ["Minimal"], "customInput": ""},
    {"id": "context", "selectedOptions": [], "customInput": "Keep the existing API"}
]
emit({"type": "prompt_result", "id": "task-prompt", "status": "completed", "sessionSettled": False})
emit({"type": "session_settled"})
assert read() == {"id": "task-summary", "type": "get_last_assistant_text"}
emit({"type": "response", "id": "task-summary", "success": True, "data": {"text": "Fix validated"}})
sys.stdin.read()
"""


class RpcTests(unittest.TestCase):
    def test_questions_resume_the_same_process_and_wait_for_session_settled(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "fake_omp.py"
            script.write_text(FAKE_OMP)
            session = rpc.OmpSession([sys.executable, str(script)], root, 10, repository=root)
            question_seen = threading.Event()
            errors = []

            def answer():
                try:
                    deadline = time.monotonic() + 5
                    while not (root / "question.json").exists():
                        if time.monotonic() > deadline:
                            raise TimeoutError("Question was not emitted")
                        time.sleep(0.01)
                    question = json.loads((root / "question.json").read_text())
                    self.assertEqual(question["id"], "ui_1")
                    question_seen.set()
                    (root / "answer.json").write_text(
                        json.dumps(
                            {
                                "question_id": "ui_1",
                                "answers": [
                                    {"id": "scope", "selected_options": ["Minimal"], "text": ""},
                                    {"id": "context", "selected_options": [], "text": "Keep the existing API"},
                                ],
                            }
                        )
                    )
                except Exception as error:
                    errors.append(error)

            thread = threading.Thread(target=answer)
            thread.start()
            self.assertEqual(session.run("Fix this"), "Fix validated")
            thread.join(timeout=5)
            self.assertTrue(question_seen.is_set())
            self.assertEqual(errors, [])
            self.assertFalse((root / "question.json").exists())
            self.assertFalse((root / "answer.json").exists())
            self.assertEqual(session.process.returncode, 0)
            events = json.loads((root / "trace.json").read_text())["events"]
            self.assertEqual(
                [event["kind"] for event in events],
                ["question", "answer_delivered", "prompt_result", "session_settled"],
            )

    def test_trace_excludes_reasoning_redacts_secrets_and_bounds_output(self):
        from unittest.mock import patch

        with (
            tempfile.TemporaryDirectory() as directory,
            patch.dict("os.environ", {"LLM_GATEWAY_API_KEY": "secret-key-for-test"}),
        ):
            root = Path(directory)
            trace = rpc.TraceRecorder(root)
            trace.capture(
                {
                    "type": "message_update",
                    "assistantMessageEvent": {"type": "thinking_delta", "delta": "private reasoning"},
                }
            )
            trace.capture(
                {
                    "type": "message_end",
                    "message": {
                        "role": "assistant",
                        "content": [
                            {"type": "thinking", "thinking": "private reasoning"},
                            {"type": "text", "text": "Checking the repo"},
                        ],
                    },
                }
            )
            trace.capture(
                {
                    "type": "tool_execution_start",
                    "toolName": "bash",
                    "toolCallId": "call_1",
                    "args": {"command": "npm test"},
                }
            )
            trace.capture(
                {
                    "type": "tool_execution_end",
                    "toolName": "bash",
                    "toolCallId": "call_1",
                    "isError": True,
                    "result": {
                        "content": [{"type": "text", "text": "secret-key-for-test Bearer abc123 token=other-secret"}]
                    },
                }
            )
            payload = json.loads((root / "trace.json").read_text())
            self.assertEqual(len(payload["events"]), 3)
            output = json.dumps(payload)
            for secret in ("private reasoning", "secret-key-for-test", "abc123", "other-secret"):
                self.assertNotIn(secret, output)
            self.assertIn("npm test", output)
            self.assertTrue(payload["events"][-1]["is_error"])
            for _ in range(300):
                trace.record("stderr", "x" * 10000)
            payload = json.loads((root / "trace.json").read_text())
            self.assertLess((root / "trace.json").stat().st_size, 500000)
            self.assertGreater(payload["events"][0]["sequence"], 1)
            self.assertTrue(payload["events"][-1]["truncated"])

    def test_unanswered_question_times_out_and_terminates_process(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "fake_omp.py"
            script.write_text(FAKE_OMP)
            session = rpc.OmpSession([sys.executable, str(script)], root, 1, repository=root)
            with self.assertRaisesRegex(TimeoutError, "deadline"):
                session.run("Fix this")
            self.assertIsNotNone(session.process.returncode)
            self.assertFalse((root / "question.json").exists())

    def test_failed_prompt_does_not_report_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / "fake_omp.py"
            script.write_text(
                FAKE_OMP[: FAKE_OMP.index('emit({"type": "extension_ui_request"')]
                + """
emit({"type": "prompt_result", "id": "task-prompt", "status": "error", "error": {"message": "Gateway failed"}})
sys.stdin.read()
"""
            )
            session = rpc.OmpSession([sys.executable, str(script)], root, 5, repository=root)
            with self.assertRaisesRegex(RuntimeError, "Gateway failed"):
                session.run("Fix this")


if __name__ == "__main__":
    unittest.main()
