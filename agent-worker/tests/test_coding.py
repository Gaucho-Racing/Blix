import os
import unittest
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import httpx

from blix_agent_worker.agents.coding import BlixCodeV1Agent
from blix_agent_worker.clients.runway import RunwayClientError
from blix_agent_worker.clients.sandbox import SandboxClient
from blix_agent_worker.models.agent import AgentResultStatus, AgentTask
from blix_agent_worker.models.common import new_ulid


class CodingAgentTests(unittest.IsolatedAsyncioTestCase):
    async def test_question_is_notified_once_and_answer_returns_to_same_task(self):
        events = []
        requests = []
        answered = False
        polls = 0
        acknowledgments = 0
        question = {"id": "ui_1", "questions": [{"id": "scope", "question": "Which scope?"}]}

        def respond(request):
            nonlocal answered, polls
            requests.append((request.method, request.url.path))
            if request.url.path.endswith("/answers"):
                answered = True
                return httpx.Response(200, json={"accepted": True})
            if request.method == "GET":
                polls += 1
                if not answered:
                    return httpx.Response(200, json={"status": "waiting_for_input", "question": question})
                return httpx.Response(
                    200,
                    json={
                        "status": "succeeded",
                        "result": {
                            "summary": "Validation passed",
                            "base_sha": "abc",
                            "patch": "",
                            "changed_files": [],
                        },
                    },
                )
            return httpx.Response(200, json={"status": "queued"})

        class Lease:
            attempt = SimpleNamespace(
                attempt_id=new_ulid("attempt"),
                deadline_at=datetime.now(UTC) + timedelta(minutes=1),
            )

            async def events(self, batch):
                events.extend(batch)

            async def post(self, operation, payload=None):
                if operation == "commands":
                    return {
                        "commands": [
                            {
                                "command_id": "command_test",
                                "kind": "answer",
                                "payload": {
                                    "question_id": "ui_1",
                                    "answers": [{"id": "scope", "text": "Minimal"}],
                                },
                            }
                        ]
                        if polls >= 2
                        else []
                    }
                nonlocal acknowledgments
                acknowledgments += 1
                if acknowledgments == 1:
                    raise RunwayClientError("Temporary database error", status_code=500)
                self.acknowledged = payload["command_ids"]
                return {}

        lease = Lease()
        client = SandboxClient("http://sandbox", "test-token", transport=httpx.MockTransport(respond))
        task = AgentTask(
            run_id=new_ulid("run"),
            session_id=new_ulid("session"),
            agent_id="blix_code_v1",
            objective="Fix main",
            input={"context": "Inspect Gaucho-Racing/Blix"},
        )
        with (
            patch.dict(os.environ, {"SANDBOX_URL": "http://sandbox"}),
            patch(
                "blix_agent_worker.agents.coding.SandboxClient",
                return_value=client,
            ),
            patch("blix_agent_worker.agents.coding.asyncio.sleep", new_callable=AsyncMock),
        ):
            result = await BlixCodeV1Agent(lease).run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(len([event for event in events if event["type"] == "runtime.question_v1"]), 1)
        self.assertEqual(lease.acknowledged, ["command_test"])
        self.assertEqual(acknowledgments, 2)
        self.assertEqual(sum(path.endswith("/answers") for _, path in requests), 1)
        paths = {path.removesuffix("/answers") for method, path in requests}
        self.assertEqual(len(paths), 1)
        self.assertEqual(requests[-1][0], "DELETE")

    async def test_trace_is_persisted_once_with_gap_and_final_events(self):
        polls = 0
        events = []
        trace = [
            {
                "sequence": 3,
                "timestamp": "2026-10-06T02:00:00Z",
                "kind": "tool_execution_start",
                "content": "npm test",
                "tool_name": "bash",
            }
        ]

        def respond(request):
            nonlocal polls
            if request.method == "GET":
                polls += 1
                if polls == 3:
                    trace.append(
                        {
                            "sequence": 4,
                            "timestamp": "2026-10-06T02:00:01Z",
                            "kind": "tool_execution_end",
                            "content": "Tests passed",
                        }
                    )
                return httpx.Response(
                    200,
                    json={
                        "status": "succeeded" if polls == 3 else "running",
                        "trace": trace,
                        "result": {"summary": "Tests passed", "base_sha": "abc", "patch": "", "changed_files": []}
                        if polls == 3
                        else None,
                    },
                )
            return httpx.Response(200, json={"status": "queued"})

        class Lease:
            attempt = SimpleNamespace(
                attempt_id=new_ulid("attempt"), deadline_at=datetime.now(UTC) + timedelta(minutes=1)
            )

            async def events(self, batch):
                events.extend(batch)

            async def post(self, operation, payload=None):
                return {"commands": []}

        task = AgentTask(
            run_id=new_ulid("run"),
            session_id=new_ulid("session"),
            agent_id="blix_code_v1",
            objective="Validate",
            input={"context": "Inspect team/repo"},
        )
        client = SandboxClient("http://sandbox", "test-token", transport=httpx.MockTransport(respond))
        with (
            patch.dict(os.environ, {"SANDBOX_URL": "http://sandbox"}),
            patch(
                "blix_agent_worker.agents.coding.SandboxClient",
                return_value=client,
            ),
            patch("blix_agent_worker.agents.coding.asyncio.sleep", new_callable=AsyncMock),
        ):
            result = await BlixCodeV1Agent(Lease()).run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        activity = [event["payload"] for event in events if event["type"] == "runtime.coding_trace_v1"]
        self.assertEqual([event["kind"] for event in activity], ["gap", "tool_execution_start", "tool_execution_end"])
        self.assertIn("2 blix_code_v1 activity events", activity[0]["content"])
        self.assertEqual(activity[-1]["content"], "Tests passed")

    async def test_specialist_returns_patch_and_always_requests_cleanup(self):
        requests = []
        recorded_events = []

        def respond(request):
            requests.append(request.method)
            if request.method == "GET":
                return httpx.Response(
                    200,
                    json={
                        "status": "succeeded",
                        "result": {
                            "summary": "Validation passed",
                            "base_sha": "abc",
                            "patch": "patch-data\n",
                            "changed_files": ["main.py"],
                        },
                    },
                )
            return httpx.Response(202 if request.method == "POST" else 200, json={"status": "queued"})

        class Lease:
            attempt = SimpleNamespace(
                attempt_id=new_ulid("attempt"),
                deadline_at=datetime.now(UTC) + timedelta(minutes=1),
            )

            async def events(self, events):
                recorded_events.extend(events)

        client = SandboxClient("http://sandbox", "test-token", transport=httpx.MockTransport(respond))
        task = AgentTask(
            run_id=new_ulid("run"),
            session_id=new_ulid("session"),
            agent_id="blix_code_v1",
            objective="Fix main",
            input={"context": "Inspect gaucho-racing/Blix"},
        )
        with (
            patch.dict(os.environ, {"SANDBOX_URL": "http://sandbox"}),
            patch(
                "blix_agent_worker.agents.coding.SandboxClient",
                return_value=client,
            ),
        ):
            result = await BlixCodeV1Agent(Lease()).run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(result.output["patch"], "patch-data\n")
        self.assertEqual(requests, ["POST", "GET", "DELETE"])
        self.assertEqual(
            recorded_events,
            [
                {
                    "type": "runtime.progress_v1",
                    "entity_id": task.session_id,
                    "payload": {"status": "succeeded"},
                }
            ],
        )


if __name__ == "__main__":
    unittest.main()


class CodingDelegationContractTests(unittest.TestCase):
    def test_lead_supplies_only_context(self):
        from blix_agent_worker.agents.coding import delegation_schema

        schema = delegation_schema()
        self.assertEqual(set(schema["properties"]), {"context"})
        self.assertFalse(schema.get("required"))
