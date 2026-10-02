import base64
import hashlib
import json
import unittest
from unittest.mock import patch

import httpx

from blix_agent_worker.clients.knowledge import KnowledgeClient, KnowledgeClientError
from blix_agent_worker.llm.document_images import hydrate_document_images
from blix_agent_worker.llm.openai_responses import OpenAIResponsesModel
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import (
    DocumentImageReference, ModelCallRequest, ModelMessage, ModelMessageRole, ModelRequest,
)
from blix_agent_worker.models.tool import ToolResult, ToolResultStatus


class DocumentImageTests(unittest.IsolatedAsyncioTestCase):
    async def test_images_are_authorized_hydrated_and_rehydrated_without_checkpoint_bytes(self):
        content = b"test-jpeg"
        image = DocumentImageReference(document_id=new_ulid("document"), page=24,
                                       sha256=hashlib.sha256(content).hexdigest())
        cid, sid = new_ulid("conversation"), new_ulid("session")
        data_url = "data:image/jpeg;base64," + base64.b64encode(content).decode()
        knowledge_calls, model_calls = [], []

        def knowledge_handler(request):
            knowledge_calls.append(request)
            self.assertEqual(request.url.params["conversation_id"], cid)
            self.assertEqual(request.url.params["session_id"], sid)
            return httpx.Response(200, json={**image.model_dump(), "data_url": data_url})

        def model_handler(request):
            payload = json.loads(request.content)
            model_calls.append(payload)
            parts = [part for item in payload["input"] if isinstance(item.get("content"), list)
                     for part in item["content"] if part.get("type") == "input_image"]
            self.assertEqual(len(parts), 1)
            self.assertEqual(parts[0]["image_url"], data_url)
            output = ([{"type": "function_call", "call_id": "read", "name": "knowledge_document_search_v1",
                        "arguments": json.dumps({"operation": "read_page", "document_id": image.document_id, "page": image.page})}]
                      if len(model_calls) == 1 else
                      [{"type": "message", "content": [{"type": "output_text", "text": "Verified the diagram."}]}])
            event = {"type": "response.completed", "response": {"status": "completed", "output": output}}
            return httpx.Response(200, headers={"Content-Type": "text/event-stream"},
                                  text="data: " + json.dumps(event) + "\n\n")

        async with KnowledgeClient(transport=httpx.MockTransport(knowledge_handler)) as knowledge:
            async with OpenAIResponsesModel(base_url="https://model.test/v1", api_key="test", model="test",
                                            transport=httpx.MockTransport(model_handler)) as model:
                with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
                    request = ModelRequest(images=(image,), messages=(ModelMessage(role="user", content="Explain the diagram."),))
                    hydrated = await hydrate_document_images(request, knowledge, cid, sid)
                    self.assertNotIn(data_url, hydrated.model_dump_json())
                    response = await model.complete(hydrated)
                    self.assertNotIn(data_url, response.model_dump_json())
                    self.assertIn(image.uri, response.continuation.model_dump_json())
                    followup = ModelRequest(continuation=response.continuation, messages=(
                        *request.messages, response.message,
                        ModelMessage(role="call_output", call_request_ref="read", content="Verified rule context."),
                    ))
                    followup = await hydrate_document_images(followup, knowledge, cid, sid)
                    response = await model.complete(followup)
                    self.assertNotIn(data_url, response.model_dump_json())
        self.assertEqual(len(knowledge_calls), 2)

    async def test_only_successful_image_tool_receipts_request_images(self):
        content = b"page"
        image = DocumentImageReference(document_id=new_ulid("document"), page=1,
                                       sha256=hashlib.sha256(content).hexdigest())
        cid, sid = new_ulid("conversation"), new_ulid("session")
        receipt = ToolResult(conversation_id=cid, parent_session_id=sid, call_id=new_ulid("call"),
                             status=ToolResultStatus.SUCCEEDED, output={"image_reference": image.model_dump()})
        request = ModelRequest(messages=(
            ModelMessage(role=ModelMessageRole.ASSISTANT, call_requests=(ModelCallRequest(
                ref="image", callable_id="knowledge_document_search_v1", arguments={"operation": "read_page_image", "document_id": image.document_id, "page": image.page}),)),
            ModelMessage(role=ModelMessageRole.CALL_OUTPUT, call_request_ref="image", content=receipt.model_dump_json()),
        ))
        calls = []

        def handler(request):
            calls.append(request)
            return httpx.Response(403)

        async with KnowledgeClient(transport=httpx.MockTransport(handler)) as knowledge:
            with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
                with self.assertRaises(KnowledgeClientError):
                    await hydrate_document_images(request, knowledge, cid, sid)
                ignored = request.model_copy(update={"messages": (
                    request.messages[0].model_copy(update={"call_requests": (request.messages[0].call_requests[0].model_copy(
                        update={"arguments": {"operation": "search"}}),)}), request.messages[1],
                )})
                result = await hydrate_document_images(ignored, knowledge, cid, sid)
                self.assertFalse(result.images)
        self.assertEqual(len(calls), 1)

    async def test_changed_image_hash_is_rejected(self):
        image = DocumentImageReference(document_id=new_ulid("document"), page=1, sha256="a" * 64)
        def handler(request):
            return httpx.Response(200, json={**image.model_dump(), "data_url": "data:image/jpeg;base64," + base64.b64encode(b"different").decode()})
        async with KnowledgeClient(transport=httpx.MockTransport(handler)) as knowledge:
            with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
                with self.assertRaisesRegex(KnowledgeClientError, "changed"):
                    await hydrate_document_images(ModelRequest(images=(image,), messages=(ModelMessage(role="user", content="Inspect"),)),
                                                  knowledge, new_ulid("conversation"), new_ulid("session"))
