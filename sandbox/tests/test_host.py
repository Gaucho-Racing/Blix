import io
import json
import os
import tarfile
import threading
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import httpx

from blix_sandbox.contracts import CodingOutput, SandboxAnswer, SandboxQuestion, SandboxRequest
from blix_sandbox.github_auth import github_token
from blix_sandbox.host import SandboxError, SandboxHost, Task


class SandboxHostTests(unittest.TestCase):
    def setUp(self):
        self.environment = patch.dict(
            os.environ,
            {
                "BLIX_INTERNAL_TOKEN": "test-token",
                "SANDBOX_REPOSITORIES": "team/repo",
                "SANDBOX_MODEL": "openai/test",
                "SANDBOX_GITHUB_TOKEN": "",
            },
        )
        self.environment.start()
        self.host = SandboxHost()
        self.addCleanup(self.host.docker.close)
        self.addCleanup(self.environment.stop)

    def request(self, **kwargs):
        return SandboxRequest(objective="Fix the test", **kwargs)

    def test_answer_reaches_existing_container_once_and_cannot_be_changed(self):
        question = SandboxQuestion.model_validate(
            {
                "id": "ui_1",
                "questions": [
                    {
                        "id": "scope",
                        "question": "Which scope?",
                        "options": [{"label": "Minimal"}],
                    }
                ],
            }
        )
        task = Task(self.request(), status="waiting_for_input", question=question)
        self.host.tasks["attempt_test"] = task
        answer = SandboxAnswer.model_validate(
            {
                "question_id": "ui_1",
                "answers": [
                    {
                        "id": "scope",
                        "selected_options": ["Minimal"],
                    }
                ],
            }
        )
        with patch.object(self.host, "engine") as engine:
            self.host.answer("attempt_test", answer)
            self.host.answer("attempt_test", answer)
            engine.assert_called_once()
            self.assertEqual(engine.call_args.args[0], "PUT")
            with tarfile.open(fileobj=io.BytesIO(engine.call_args.kwargs["content"])) as archive:
                payload = json.load(archive.extractfile("answer.json"))
            self.assertEqual(payload["question_id"], "ui_1")
            self.assertEqual(task.status, "running")
            self.assertIsNone(task.question)
            with patch.object(self.host, "read_json", return_value=question.model_dump(mode="json")):
                self.host.refresh_question("container", task)
            self.assertEqual(task.status, "running")
            different = SandboxAnswer(question_id="ui_1", answers=[{"id": "scope", "text": "Different"}])
            with self.assertRaisesRegex(ValueError, "different answer"):
                self.host.answer("attempt_test", different)

    def test_trace_is_retained_after_container_cleanup(self):
        task = Task(self.request())
        trace = {
            "events": [
                {
                    "sequence": 1,
                    "timestamp": "2026-10-06T02:00:00Z",
                    "kind": "stderr",
                    "content": "Gateway failed",
                    "is_error": True,
                }
            ]
        }
        with patch.object(self.host, "read_json", return_value=trace):
            self.host.refresh_trace("container", task)
        self.assertEqual(task.trace[0].content, "Gateway failed")
        with patch.object(self.host, "read_json", side_effect=SandboxError("Container already removed")):
            with self.assertLogs("blix_sandbox.host", level="WARNING"):
                self.host.refresh_trace("container", task)
        self.assertEqual(task.trace[0].sequence, 1)

    def test_credential_grants_are_cached_renewed_and_expire_with_task(self):
        from datetime import UTC, datetime, timedelta

        task = Task(self.request(), status="running")
        self.host.tasks["attempt_test"] = task
        credentials = {
            "token": "installation-token",
            "expires_at": (datetime.now(UTC) + timedelta(hours=1)).isoformat(),
        }
        grant = "Bearer " + task.credential_secret
        with (
            patch("blix_sandbox.host.github_credentials", return_value=credentials) as mint,
            patch("blix_sandbox.host.github_app_configured", return_value=True),
        ):
            self.assertEqual(self.host.credentials("attempt_test", grant)["token"], "installation-token")
            self.host.credentials("attempt_test", grant)
            self.assertEqual(mint.call_count, 1)
            credentials["expires_at"] = (datetime.now(UTC) + timedelta(minutes=1)).isoformat()
            self.host.credentials("attempt_test", grant)
            self.assertEqual(mint.call_count, 2)
            with self.assertRaises(PermissionError):
                self.host.credentials("attempt_test", "Bearer wrong-task-secret")
            task.status = "succeeded"
            with self.assertRaises(PermissionError):
                self.host.credentials("attempt_test", grant)
            self.assertEqual(mint.call_count, 2)

    def test_write_identity_is_resolved_from_verified_github_id(self):
        request = self.request(
            requester_github_id=123,
            requester_name="Test User",
            requester_github_login="spoofed",
            requester_email="requester@gauchoracing.com",
        )
        with (
            patch("blix_sandbox.host.github_app_configured", return_value=True),
            patch("blix_sandbox.host.github_token", return_value="read-token"),
            patch(
                "blix_sandbox.host.httpx.get", return_value=httpx.Response(200, json={"id": 123, "login": "requester"})
            ) as lookup,
            patch.object(self.host, "execute"),
        ):
            task = self.host.submit("attempt_test", request)
        self.assertEqual(lookup.call_args.args[0], "https://api.github.com/user/123")
        self.assertEqual(task.request.requester_github_login, "requester")
        self.assertEqual(task.request.requester_email, "requester@gauchoracing.com")

    def test_write_requires_verified_link_before_starting_sandbox(self):
        with (
            patch("blix_sandbox.host.github_app_configured", return_value=True),
            patch.object(self.host, "execute") as execute,
        ):
            with self.assertRaisesRegex(ValueError, "verified linked"):
                self.host.submit(
                    "attempt_test", self.request(requester_github_id=123, requester_github_login="spoofed")
                )
            execute.assert_not_called()

    def test_read_and_write_credential_caches_are_separate(self):
        from datetime import UTC, datetime, timedelta

        credentials = {"token": "token", "expires_at": (datetime.now(UTC) + timedelta(hours=1)).isoformat()}
        with (
            patch("blix_sandbox.host.github_credentials", return_value=credentials) as mint,
            patch("blix_sandbox.host.github_app_configured", return_value=True),
        ):
            for task_id, write in (("attempt_write", True), ("attempt_read", False)):
                task = Task(self.request(requester_github_id=123 if write else None), status="running")
                self.host.tasks[task_id] = task
                self.host.credentials(task_id, "Bearer " + task.credential_secret)
                mint.assert_called_with(write=write)
            self.assertEqual(mint.call_count, 2)

    def test_stale_or_invalid_answer_cannot_resume_task(self):
        question = SandboxQuestion.model_validate(
            {
                "id": "ui_1",
                "questions": [
                    {
                        "id": "scope",
                        "question": "Which scope?",
                        "options": [{"label": "Minimal"}],
                    }
                ],
            }
        )
        self.host.tasks["attempt_test"] = Task(self.request(), status="waiting_for_input", question=question)
        for payload in (
            {"question_id": "old", "answers": [{"id": "scope", "text": "Minimal"}]},
            {"question_id": "ui_1", "answers": [{"id": "other", "text": "Minimal"}]},
            {"question_id": "ui_1", "answers": [{"id": "scope", "selected_options": ["Unknown"]}]},
            {"question_id": "ui_1", "answers": [{"id": "scope", "selected_options": ["Minimal"], "text": "Both"}]},
        ):
            with patch.object(self.host, "engine") as engine:
                with self.assertRaises(ValueError):
                    self.host.answer("attempt_test", SandboxAnswer.model_validate(payload))
                engine.assert_not_called()
        self.host.tasks["attempt_test"].cancelled.set()
        with self.assertRaisesRegex(ValueError, "not waiting"):
            self.host.answer(
                "attempt_test",
                SandboxAnswer(
                    question_id="ui_1",
                    answers=[{"id": "scope", "text": "Minimal"}],
                ),
            )

    def test_linked_user_requires_configured_github_credentials(self):
        with patch("blix_sandbox.host.github_app_configured", return_value=False):
            with self.assertRaisesRegex(ValueError, "GitHub write tasks"):
                self.host.submit("attempt_test", self.request(requester_github_id=123))

    def test_duplicate_request_is_not_executed_twice(self):
        with patch("threading.Thread") as thread:
            first = self.host.submit("attempt_test", self.request())
            second = self.host.submit("attempt_test", self.request())
        self.assertIs(first, second)
        thread.assert_called_once()
        with self.assertRaisesRegex(ValueError, "different request"):
            self.host.submit("attempt_test", self.request(context="Use develop"))

    def test_failure_still_removes_container_and_volume(self):
        requests = []

        def engine(request):
            requests.append((request.method, request.url.path))
            return httpx.Response(204)

        self.host.docker.close()
        self.host.docker = httpx.Client(base_url="http://docker", transport=httpx.MockTransport(engine))
        task = Task(self.request())
        with (
            patch.object(self.host, "upload", side_effect=SandboxError("upload failed")),
            patch.object(self.host, "engine"),
        ):
            self.host.execute("attempt_test", task)
        self.assertEqual(task.status, "failed")
        self.assertIn("upload failed", task.error)
        self.assertEqual([method for method, _ in requests], ["DELETE", "DELETE"])

    def test_cancelled_queued_task_is_not_started(self):
        task = Task(self.request(), cancelled=threading.Event())
        task.cancelled.set()
        with patch.object(self.host, "engine") as engine, patch.object(self.host, "remove") as remove:
            self.host.execute("attempt_test", task)
        engine.assert_not_called()
        remove.assert_called_once()
        self.assertEqual(task.status, "cancelled")

    def test_result_archive_rejects_symlinks(self):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as tar:
            member = tarfile.TarInfo("result.json")
            member.type = tarfile.SYMTYPE
            member.linkname = "/etc/passwd"
            tar.addfile(member)
        self.host.docker.close()
        self.host.docker = httpx.Client(
            base_url="http://docker",
            transport=httpx.MockTransport(
                lambda request: httpx.Response(200, content=archive.getvalue()),
            ),
        )
        with self.assertRaisesRegex(SandboxError, "Invalid sandbox result"):
            self.host.result("test")

    def test_app_token_defaults_to_all_installation_repositories_and_write_permissions(self):
        with (
            patch.dict(
                os.environ,
                {
                    "GITHUB_APP_ID": "123",
                    "GITHUB_APP_INSTALLATION_ID": "456",
                    "GITHUB_APP_PRIVATE_KEY": "fake-key",
                },
            ),
            patch(
                "blix_sandbox.github_auth.subprocess.run",
                return_value=SimpleNamespace(
                    returncode=0,
                    stdout=b"signature",
                ),
            ),
            patch(
                "blix_sandbox.github_auth.httpx.post",
                return_value=httpx.Response(
                    201,
                    json={"token": "scoped-token", "expires_at": "2026-10-06T23:00:00Z"},
                ),
            ) as post,
            patch(
                "blix_sandbox.github_auth.httpx.get",
                side_effect=[
                    httpx.Response(200, json={"slug": "blix"}),
                    httpx.Response(200, json={"id": 12345}),
                ],
            ),
        ):
            self.assertEqual(github_token(write=True), "scoped-token")
        self.assertEqual(
            post.call_args.kwargs["json"],
            {
                "permissions": {"contents": "write", "pull_requests": "write"},
            },
        )

    def test_read_token_has_only_contents_read_permission(self):
        with (
            patch.dict(
                os.environ,
                {
                    "GITHUB_APP_ID": "123",
                    "GITHUB_APP_INSTALLATION_ID": "456",
                    "GITHUB_APP_PRIVATE_KEY": "fake-key",
                },
            ),
            patch(
                "blix_sandbox.github_auth.subprocess.run",
                return_value=SimpleNamespace(
                    returncode=0,
                    stdout=b"signature",
                ),
            ),
            patch(
                "blix_sandbox.github_auth.httpx.post",
                return_value=httpx.Response(
                    201,
                    json={"token": "scoped-token", "expires_at": "2026-10-06T23:00:00Z"},
                ),
            ) as post,
            patch(
                "blix_sandbox.github_auth.httpx.get",
                side_effect=[
                    httpx.Response(200, json={"slug": "blix"}),
                    httpx.Response(200, json={"id": 12345}),
                ],
            ),
        ):
            self.assertEqual(github_token(write=False), "scoped-token")
        self.assertEqual(
            post.call_args.kwargs["json"],
            {
                "permissions": {"contents": "read"},
            },
        )

    def test_result_is_validated_before_cleanup(self):
        archive = io.BytesIO()
        payload = json.dumps(
            {
                "summary": "Tests passed",
                "base_sha": "abc",
                "patch": "+source\n",
                "changed_files": [],
            }
        ).encode()
        with tarfile.open(fileobj=archive, mode="w") as tar:
            member = tarfile.TarInfo("result.json")
            member.size = len(payload)
            tar.addfile(member, io.BytesIO(payload))
        self.host.docker.close()
        self.host.docker = httpx.Client(
            base_url="http://docker",
            transport=httpx.MockTransport(
                lambda request: httpx.Response(200, content=archive.getvalue()),
            ),
        )
        result = self.host.result("test")
        self.assertEqual(result.summary, "Tests passed")
        self.assertEqual(result.patch, "+source\n")

    def test_write_task_without_changes_or_pr_succeeds(self):
        task = Task(self.request())
        output = CodingOutput(
            summary="No source fix justified; proxy headers require investigation.",
            base_sha="abc",
            patch="",
            changed_files=[],
        )
        with (
            patch.object(
                self.host,
                "engine",
                return_value=SimpleNamespace(
                    json=lambda: {"State": {"Running": False}},
                ),
            ),
            patch.object(self.host, "upload"),
            patch.object(self.host, "result", return_value=output),
            patch.object(self.host, "refresh_trace"),
            patch.object(self.host, "remove") as remove,
        ):
            self.host.execute("attempt_test", task)
        self.assertEqual(task.status, "succeeded")
        self.assertIsNone(task.error)
        self.assertEqual(task.result["summary"], output.summary)
        self.assertIsNone(task.result["pr_url"])
        remove.assert_called_once_with("attempt_test")

    def test_published_pr_is_returned_without_mutating_the_frozen_result(self):
        task = Task(self.request(requester_github_id=123))
        output = CodingOutput(
            summary="Tests passed",
            base_sha="abc",
            patch="+source\n",
            changed_files=["main.py"],
            pr_url="https://github.com/team/repo/pull/1",
        )

        def engine(method, path, **kwargs):
            return SimpleNamespace(json=lambda: {"State": {"Running": False}})

        with (
            patch.object(self.host, "engine", side_effect=engine),
            patch.object(self.host, "upload"),
            patch.object(self.host, "result", return_value=output),
            patch.object(self.host, "refresh_trace"),
            patch.object(self.host, "remove"),
        ):
            self.host.execute("attempt_test", task)
        self.assertEqual(task.status, "succeeded")
        self.assertEqual(task.result["pr_url"], "https://github.com/team/repo/pull/1")
        self.assertEqual(output.pr_url, "https://github.com/team/repo/pull/1")


if __name__ == "__main__":
    unittest.main()
