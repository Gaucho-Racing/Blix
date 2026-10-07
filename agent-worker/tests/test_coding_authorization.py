import os
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

from blix_agent_worker.models.agent import AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.context import EntityType
from blix_agent_worker.worker import ApiRuntime, CallerVerificationError


class CodingAuthorizationTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        public_url = patch.dict(os.environ, {"BLIX_PUBLIC_URL": "https://blix.example/"})
        public_url.start()
        self.addCleanup(public_url.stop)
        self.runtime = ApiRuntime.__new__(ApiRuntime)
        self.runtime.sentinel = SimpleNamespace(resolve_entity_identity=AsyncMock())
        self.caller = SimpleNamespace(
            id=new_ulid("usr"),
            entity_id=new_ulid("ent"),
            first_name="Test",
            last_name="User",
            username="requester",
            email="requester@example.com",
        )

    def task(self, **arguments):
        return AgentTask(
            run_id=new_ulid("run"),
            session_id=new_ulid("session"),
            agent_id="blix_code_v1",
            objective="Inspect repository",
            input={"context": "Inspect team/repo", **arguments},
        )

    def identity(self, links):
        return SimpleNamespace(
            id=self.caller.entity_id,
            type=EntityType.USER,
            user=self.caller,
            external_auths=links,
        )

    async def test_reading_does_not_require_a_github_link(self):
        self.runtime.sentinel.resolve_entity_identity.return_value = self.identity([])
        result = await self.runtime.authorize_coding_task(
            self.task(requester_github_id=999, requester_github_login="spoofed"),
            self.caller,
        )
        self.runtime.sentinel.resolve_entity_identity.assert_awaited_once()
        self.assertIsNone(result.input["requester_github_id"])
        self.assertIsNone(result.input["requester_github_login"])

    async def test_unlinked_requester_does_not_receive_write_identity(self):
        self.runtime.sentinel.resolve_entity_identity.return_value = self.identity([])
        result = await self.runtime.authorize_coding_task(self.task(), self.caller)
        self.assertIsNone(result.input["requester_github_id"])

    async def test_write_uses_verified_identity_instead_of_supplied_attribution(self):
        self.runtime.sentinel.resolve_entity_identity.return_value = self.identity(
            [
                SimpleNamespace(provider="GITHUB", external_id="123"),
            ]
        )
        result = await self.runtime.authorize_coding_task(
            self.task(
                requester_github_id=999,
                requester_github_login="spoofed",
                requester_name="Someone Else",
                requester_email="other@example.com",
                blix_run_url="https://wrong.example/chat/fake",
            ),
            self.caller,
        )
        self.assertEqual(result.input["requester_github_id"], 123)
        self.assertEqual(result.input["requester_name"], "Test User")
        self.assertEqual(result.input["requester_email"], self.caller.email)
        self.assertEqual(result.input["blix_run_url"], f"https://blix.example/chat/{result.run_id}")
        self.assertIsNone(result.input["requester_github_login"])

    async def test_ambiguous_or_invalid_links_are_rejected(self):
        for ids in (["invalid"], ["0"], ["123", "456"]):
            self.runtime.sentinel.resolve_entity_identity.return_value = self.identity(
                [SimpleNamespace(provider="GITHUB", external_id=value) for value in ids]
            )
            with self.subTest(ids=ids), self.assertRaises(CallerVerificationError):
                await self.runtime.authorize_coding_task(self.task(), self.caller)
