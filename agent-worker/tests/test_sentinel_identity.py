import unittest
from unittest.mock import patch

import httpx
from pydantic import ValidationError

from blix_agent_worker.clients.sentinel import SentinelClient, SentinelClientError
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.tool import ToolCall, ToolResultStatus
from blix_agent_worker.tools.sentinel_identity.sentinel_identity_resolve_v1 import (
    SentinelIdentityResolveV1Input,
    SentinelIdentityResolveV1Tool,
)


def identity_payload(entity_id):
    return {
        "id": entity_id,
        "type": "USER",
        "user": {"id": new_ulid("usr"), "entity_id": entity_id, "first_name": "Bharat", "last_name": "Kathi"},
        "external_auths": [{
            "entity_id": entity_id, "provider": "DISCORD", "external_id": "123",
            "access_token": "private-provider-token",
        }],
    }


class SentinelIdentityTests(unittest.IsolatedAsyncioTestCase):
    async def test_entity_lookup_returns_links_without_credentials_or_impersonation(self):
        entity_id = new_ulid("ent")

        def handler(request):
            self.assertEqual(request.url.path, f"/api/core/entity/{entity_id}")
            self.assertEqual(request.headers["Authorization"], "Bearer service-token")
            return httpx.Response(200, json=identity_payload(entity_id))

        async with SentinelClient(transport=httpx.MockTransport(handler)) as client:
            tool = SentinelIdentityResolveV1Tool(client)
            call = ToolCall(
                run_id=new_ulid("run"), parent_session_id=new_ulid("session"), call_id=new_ulid("call"),
                tool_id=tool.definition.id, arguments={"entity_id": entity_id},
            )
            with patch.dict("os.environ", {
                "SENTINEL_URL": "https://sentinel.example", "SENTINEL_SERVICE_ACCOUNT_TOKEN": "service-token",
            }):
                result = await tool.execute(call)
        self.assertEqual(result.status, ToolResultStatus.SUCCEEDED)
        self.assertEqual(result.output["entity_id"], entity_id)
        self.assertEqual(result.output["full_name"], "Bharat Kathi")
        self.assertEqual(result.output["linked_ids"], [{"provider": "discord", "external_id": "123"}])
        self.assertNotIn("private-provider-token", result.model_dump_json())
        self.assertNotIn("service-token", result.model_dump_json())

    async def test_mismatched_entity_or_link_is_rejected(self):
        entity_id = new_ulid("ent")
        wrong_entity = identity_payload(new_ulid("ent"))
        wrong_link = identity_payload(entity_id)
        wrong_link["external_auths"][0]["entity_id"] = new_ulid("ent")
        for payload in (wrong_entity, wrong_link):
            with self.subTest(payload=payload):
                async with SentinelClient(transport=httpx.MockTransport(
                    lambda _: httpx.Response(200, json=payload)
                )) as client:
                    with self.assertRaises(SentinelClientError) as raised:
                        await client.resolve_entity_identity(
                            sentinel_url="https://sentinel.example", service_account_token="service-token",
                            entity_id=entity_id,
                        )
                    self.assertEqual(raised.exception.code, "invalid_sentinel_response")

    async def test_missing_entity_and_unlinked_user(self):
        entity_id = new_ulid("ent")
        payload = identity_payload(entity_id)
        payload["external_auths"] = []
        for status in (404, 200):
            async with SentinelClient(transport=httpx.MockTransport(
                lambda _: httpx.Response(status, json=payload)
            )) as client:
                identity = await client.resolve_entity_identity(
                    sentinel_url="https://sentinel.example", service_account_token="service-token",
                    entity_id=entity_id,
                )
            if status == 404:
                self.assertIsNone(identity)
            else:
                self.assertEqual(identity.external_auths, ())

    async def test_external_lookup_still_verifies_requested_discord_id(self):
        entity_id = new_ulid("ent")
        async with SentinelClient(transport=httpx.MockTransport(
            lambda _: httpx.Response(200, json=identity_payload(entity_id))
        )) as client:
            identity = await client.resolve_external_identity(
                sentinel_url="https://sentinel.example", service_account_token="service-token",
                provider="discord", external_id="123",
            )
            self.assertEqual(identity.id, entity_id)
            with self.assertRaises(SentinelClientError):
                await client.resolve_external_identity(
                    sentinel_url="https://sentinel.example", service_account_token="service-token",
                    provider="discord", external_id="999",
                )

    def test_lookup_requires_one_unambiguous_selector(self):
        entity_id = new_ulid("ent")
        SentinelIdentityResolveV1Input(entity_id=entity_id)
        SentinelIdentityResolveV1Input(provider="discord", external_id="123")
        for arguments in ({}, {"provider": "discord"}, {"external_id": "123"}, {
            "entity_id": entity_id, "provider": "discord", "external_id": "123",
        }):
            with self.subTest(arguments=arguments), self.assertRaises(ValidationError):
                SentinelIdentityResolveV1Input(**arguments)
