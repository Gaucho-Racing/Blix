import unittest
from unittest.mock import patch

from pydantic import ValidationError

from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.tool import ToolCall, ToolResultStatus
from blix_agent_worker.tools.knowledge.knowledge_document_search_v1 import (
    KnowledgeDocumentSearchV1Input,
    KnowledgeDocumentSearchV1Tool,
)


class DocumentSearchTests(unittest.IsolatedAsyncioTestCase):
    async def test_search_uses_trusted_run_and_session(self):
        requests = []

        class Client:
            async def request(self, url, method, path, **kwargs):
                requests.append((method, path, kwargs))
                return {"results": []}

        tool = KnowledgeDocumentSearchV1Tool(Client())
        call = ToolCall(
            run_id=new_ulid("run"),
            parent_session_id=new_ulid("session"),
            call_id=new_ulid("call"),
            tool_id=tool.definition.id,
            arguments={"operation": "search", "query": "T.3.2 brakes"},
        )
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await tool.execute(call)
        self.assertEqual(result.status, ToolResultStatus.SUCCEEDED)
        self.assertEqual(requests[0][0:2], ("GET", "document-evidence"))
        params = requests[0][2]["params"]
        self.assertEqual(params["run_id"], call.run_id)
        self.assertEqual(params["session_id"], call.parent_session_id)
        self.assertEqual(params["scope"], "team")
        self.assertEqual(params["q"], "T.3.2 brakes")

    def test_invalid_operations_and_scope_overrides_are_rejected(self):
        for arguments in (
            {"operation": "search"},
            {"operation": "search", "query": "brakes", "page": 1},
            {"operation": "search", "query": "brakes", "run_id": new_ulid("run")},
            {"operation": "read_page", "page": 1},
            {"operation": "list_documents", "document_id": new_ulid("document")},
            {"operation": "search", "query": "brakes", "scope": "all"},
        ):
            with self.subTest(arguments=arguments), self.assertRaises(ValidationError):
                KnowledgeDocumentSearchV1Input(**arguments)
