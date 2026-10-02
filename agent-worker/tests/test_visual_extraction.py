import asyncio
import json
import unittest

from blix_agent_worker.agents.document_ingest.visual_extraction import DocumentVisualExtractor
from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.agent import AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole, ModelResponse


class VisualExtractionTests(unittest.IsolatedAsyncioTestCase):
    def fixture(self, pages=7, concurrency=4):
        task = AgentTask(run_id=new_ulid("run"), session_id=new_ulid("session"),
            agent_id="document_ingest_v1", objective="Read this document.",
            input={"document_id": new_ulid("document"), "generation": 1})
        details = {"pages": [{"page_number": number, "source_record_id": new_ulid("sourcerecord"),
                              "image_sha256": "a" * 64, "visual_analyzed_at": None}
                             for number in range(1, pages + 1)]}
        calls, requests = [], []

        class Client:
            async def request(self, base_url, method, path, **kwargs):
                calls.append((method, path, kwargs))
                number = int(path.split("/pages/")[1].split("/")[0])
                if method == "GET":
                    return {"source_record_id": details["pages"][number - 1]["source_record_id"],
                            "content": "F.1.13 Envelope and dimensions."}
                details["pages"][number - 1]["visual_analyzed_at"] = "saved"
                return details["pages"][number - 1]

        class Model:
            active = 0
            maximum = 0

            async def complete(self, request):
                requests.append(request)
                self.active += 1
                self.maximum = max(self.maximum, self.active)
                await asyncio.sleep(0)
                self.active -= 1
                number = request.images[0].page
                finding = {"page": number, "section_ids": ["F.1.13"],
                           "description": "The red dashed plane connects the labeled hoops."} if number != 2 else None
                return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT,
                                                          content=json.dumps({"finding": finding})))

        async def on_response(*args):
            pass

        extractor = DocumentVisualExtractor(client=Client(), model=Model(), model_name="fast-vision",
                                             concurrency=concurrency, on_response=on_response)
        return extractor, task, details, calls, requests

    async def test_visual_pass_is_bounded_and_saves_text_only_pages_without_fake_findings(self):
        extractor, task, details, calls, requests = self.fixture()
        await extractor.extract(task, details, "http://knowledge", "documents/doc", {"generation": 1})
        self.assertEqual(extractor.model.maximum, 4)
        self.assertEqual(len(requests), 7)
        self.assertTrue(all(len(request.images) == 1 for request in requests))
        saves = [call for call in calls if call[0] == "POST"]
        self.assertEqual(len(saves), 7)
        text_only = next(call for call in saves if "/pages/2/" in call[1])
        self.assertIsNone(text_only[2]["payload"]["finding"])
        self.assertTrue(all(call[2]["payload"]["model"] == "fast-vision" for call in saves))
        self.assertTrue(all(page["visual_analyzed_at"] for page in details["pages"]))
        self.assertNotIn("notes", details)
        self.assertFalse(any("chunks" in call[1] for call in calls))
        calls.clear()
        await extractor.extract(task, details, "http://knowledge", "documents/doc", {"generation": 2})
        self.assertEqual(calls, [])
        self.assertEqual(len(requests), 7)

    async def test_failed_page_cancels_and_awaits_other_page_requests(self):
        extractor, task, details, calls, _ = self.fixture(pages=2, concurrency=2)
        both_started = asyncio.Event()
        cancelled = asyncio.Event()
        started = 0

        async def complete(request):
            nonlocal started
            started += 1
            if started == 2:
                both_started.set()
            await both_started.wait()
            if request.images[0].page == 1:
                raise KnowledgeClientError("unavailable", "Try again.", retryable=True)
            try:
                await asyncio.Event().wait()
            finally:
                cancelled.set()

        extractor.model.complete = complete
        with self.assertRaises(KnowledgeClientError):
            await asyncio.wait_for(extractor.extract(task, details, "http://knowledge", "documents/doc", {}), 1)
        self.assertTrue(cancelled.is_set())
        self.assertFalse(any(call[0] == "POST" for call in calls))

    async def test_a_description_cannot_claim_a_different_page(self):
        extractor, task, details, calls, _ = self.fixture(pages=1)
        attempts = 0

        async def complete(request):
            nonlocal attempts
            attempts += 1
            return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
                "finding": {"page": 24, "description": "Invented diagram", "section_ids": []},
            })))

        extractor.model.complete = complete
        with self.assertRaises(KnowledgeClientError) as error:
            await extractor.extract(task, details, "http://knowledge", "documents/doc", {})
        self.assertEqual(error.exception.code, "invalid_visual_analysis")
        self.assertEqual(attempts, 3)
        self.assertFalse(any(call[0] == "POST" for call in calls))
