import unittest
from unittest.mock import patch

from pydantic import ValidationError

from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.tool import ToolCall, ToolResultStatus
from blix_agent_worker.tools.knowledge.knowledge_read_v1 import KnowledgeReadV1Input, KnowledgeReadV1Tool


class KnowledgeReadTests(unittest.IsolatedAsyncioTestCase):
    async def test_section_and_cursor_are_sent_with_revision(self):
        requests = []

        class Client:
            async def request(self, url, method, path, **kwargs):
                requests.append((method, path, kwargs))
                return {"page": {"content": "Evidence"}, "truncated": False}

        tool = KnowledgeReadV1Tool(Client())
        call = ToolCall(
            conversation_id=new_ulid("conversation"),
            parent_session_id=new_ulid("session"),
            call_id=new_ulid("call"),
            tool_id=tool.definition.id,
            arguments={
                "operation": "read_page",
                "path": "seasons/gr26/brakes.md",
                "section": 0,
                "cursor": 0,
                "commit_sha": "a" * 40,
            },
        )
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await tool.execute(call)
        self.assertEqual(result.status, ToolResultStatus.SUCCEEDED)
        self.assertEqual(requests[0][2]["params"]["section"], 0)
        self.assertEqual(requests[0][2]["params"]["commit_sha"], "a" * 40)

    def test_operation_validation(self):
        KnowledgeReadV1Input(operation="read_page", path="team/index.md")
        KnowledgeReadV1Input(operation="search", query="braking", season="gr26", prefix="seasons/")
        for arguments in (
            {"operation": "search", "query": "braking", "cursor": 1},
            {"operation": "read_page", "path": "team/index.md", "commit_sha": "invalid"},
            {"operation": "read_page"},
            {"operation": "read_page", "path": "team/index.md", "max_bytes": 32001},
        ):
            with self.subTest(arguments=arguments), self.assertRaises(ValidationError):
                KnowledgeReadV1Input(**arguments)
