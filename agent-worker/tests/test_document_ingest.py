import asyncio
import copy
import json
import unittest
from unittest.mock import patch

from pydantic import ValidationError

from blix_agent_worker.agents.blix import BlixV1Input
from blix_agent_worker.agents.document_ingest.document_ingest_v1 import DocumentIngestV1Agent, ExtractedDocumentMetadata
from blix_agent_worker.agents.knowledge_curator.knowledge_curator_v1 import (
    KnowledgeCuratorV1Agent,
    KnowledgeCuratorV1Output,
)
from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.agent import AgentResultStatus, AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import ModelCallRequest, ModelMessage, ModelMessageRole, ModelResponse
from blix_agent_worker.models.tool import ToolResult, ToolResultStatus


class DocumentIngestionTests(unittest.IsolatedAsyncioTestCase):
    def test_document_curator_uses_focused_evidence_without_a_reading_notebook(self):
        document_id = new_ulid("document")
        sources = [new_ulid("sourcerecord"), new_ulid("sourcerecord")]
        changeset_id = new_ulid("changeset")
        task = AgentTask(
            run_id=new_ulid("run"), session_id=new_ulid("session"),
            agent_id="knowledge_curator_v1", objective="Curate the relevant requirement.",
            input={"document_id": document_id, "source_record_ids": sources[:1]},
        )
        messages = []

        def receipt(tool_id, arguments, output):
            reference = str(len(messages))
            messages.append(ModelMessage(
                role=ModelMessageRole.ASSISTANT,
                call_requests=(ModelCallRequest(ref=reference, callable_id=tool_id, arguments=arguments),),
            ))
            messages.append(ModelMessage(
                role=ModelMessageRole.CALL_OUTPUT, call_request_ref=reference,
                content=ToolResult(
                    run_id=task.run_id, parent_session_id=task.session_id,
                    call_id=new_ulid("call"), status=ToolResultStatus.SUCCEEDED, output=output,
                ).model_dump_json(),
            ))

        manifest = {"document": {"document_id": document_id},
                    "pages": [{"source_record_id": source} for source in sources],
                    "chunks": [{"completed_at": None}], "notes": None}
        receipt("knowledge_document_read_v1", {"document_id": document_id}, manifest)
        receipt("knowledge_propose_v1", {}, {
            "changeset": {"changeset_id": changeset_id, "run_id": task.run_id,
                          "session_id": task.session_id, "status": "proposed"},
            "files": [{"path": "reference.md"}], "source_record_ids": [sources[0]],
        })
        output = KnowledgeCuratorV1Output(
            summary="Proposed a cited reference.", outcome="proposed", source_record_ids=tuple(sources[:1]),
            changesets=({"changeset_id": changeset_id, "status": "proposed", "commit_sha": "",
                        "paths": ("reference.md",)},), warnings=(),
        )
        curator = KnowledgeCuratorV1Agent.__new__(KnowledgeCuratorV1Agent)
        with self.assertRaisesRegex(ValueError, "read its source records"):
            curator.validate_output(task, output, messages)
        receipt("knowledge_evidence_read_v1", {"source_record_id": sources[0]}, {"source_record_id": sources[0]})
        curator.validate_output(task, output, messages)
        foreign = copy.deepcopy(messages)
        result = ToolResult.model_validate_json(foreign[1].content)
        result.output["pages"] = [{"source_record_id": sources[1]}]
        foreign[1] = foreign[1].model_copy(update={"content": result.model_dump_json()})
        with self.assertRaisesRegex(ValueError, "belonging to the supplied document"):
            curator.validate_output(task, output, foreign)

    def test_visual_curation_requires_the_original_page_even_without_document_input(self):
        original, visual = new_ulid("sourcerecord"), new_ulid("sourcerecord")
        task = AgentTask(run_id=new_ulid("run"), session_id=new_ulid("session"),
                         agent_id="knowledge_curator_v1", objective="Remember the envelope diagram.",
                         input={"source_record_ids": [original, visual]})
        changeset_id = new_ulid("changeset")
        messages = []

        def receipt(tool_id, output):
            reference = str(len(messages))
            messages.extend((ModelMessage(role=ModelMessageRole.ASSISTANT,
                call_requests=(ModelCallRequest(ref=reference, callable_id=tool_id, arguments={}),)),
                ModelMessage(role=ModelMessageRole.CALL_OUTPUT, call_request_ref=reference,
                    content=ToolResult(run_id=task.run_id, parent_session_id=task.session_id,
                        call_id=new_ulid("call"), status=ToolResultStatus.SUCCEEDED, output=output).model_dump_json())))

        receipt("knowledge_evidence_read_v1", {"source_record_id": visual, "capture_method": "document_vision",
                "metadata": {"original_source_record_id": original}})
        receipt("knowledge_propose_v1", {
            "changeset": {"changeset_id": changeset_id, "run_id": task.run_id,
                          "session_id": task.session_id, "status": "proposed"},
            "files": [{"path": "reference.md"}], "source_record_ids": [visual],
        })
        output = KnowledgeCuratorV1Output(summary="Proposed a cited reference.", outcome="proposed",
            source_record_ids=(original, visual), changesets=({"changeset_id": changeset_id,
                "status": "proposed", "commit_sha": "", "paths": ("reference.md",)},), warnings=())
        curator = KnowledgeCuratorV1Agent.__new__(KnowledgeCuratorV1Agent)
        with self.assertRaisesRegex(ValueError, "read its source records"):
            curator.validate_output(task, output, messages)
        receipt("knowledge_evidence_read_v1", {"source_record_id": original})
        curator.validate_output(task, output, messages)

    def test_completion_notification_can_start_a_lead_turn_without_prior_chat(self):
        BlixV1Input(messages=(), notifications=({"task_id": "task", "status": "succeeded"},))
        with self.assertRaises(ValidationError):
            BlixV1Input(messages=(), notifications=())

    def fixture(self, *, fail_after_finish=False):
        document_id = new_ulid("document")
        sources = [new_ulid("sourcerecord"), new_ulid("sourcerecord")]
        task = AgentTask(
            run_id=new_ulid("run"), session_id=new_ulid("session"),
            parent_session_id=new_ulid("session"), agent_id="document_ingest_v1",
            objective="Prepare searchable evidence and document details.",
            input={"document_id": document_id, "generation": 1},
        )
        details = {
            "document": {"document_id": document_id, "title": "Official rules", "status": "analyzing_visuals"},
            "pages": [{"page_number": i + 1, "source_record_id": source,
                       "image_sha256": "a" * 64} for i, source in enumerate(sources)],
            "chunks": [{"number": i + 1, "completed_at": None} for i in range(2)],
            "notes": None, "changesets": [],
        }
        calls, requests = [], []

        class Client:
            async def request(self, url, method, path, *, payload=None, params=None, timeout=60):
                calls.append((method, path, payload))
                if method == "GET" and "/chunks/" in path:
                    number = int(path.rsplit("/", 1)[-1])
                    return {"chunk": {"number": number, "first_page": number, "last_page": number},
                            "evidence": [{"page": number, "source_record_id": sources[number - 1],
                                          "content": "Formula SAE Rules. SAE International."}]}
                if path.endswith("/metadata"):
                    details["document"].update(payload["metadata"])
                    details["document"]["extracted_metadata"] = payload["metadata"]
                if path.endswith("/finish"):
                    assert details["document"].get("extracted_metadata")
                    assert all(page.get("visual_analyzed_at") for page in details["pages"])
                    details["document"]["status"] = "ready"
                    details["document"]["review_status"] = "in_review"
                    if fail_after_finish:
                        raise KnowledgeClientError("knowledge_unavailable", "Response was lost.", retryable=True)
                return copy.deepcopy(details)

        class Model:
            async def prepare(self, request):
                return request

            async def complete(self, request):
                requests.append(request)
                assert request.output_format.id == "document_metadata_v1"
                body, _ = json.JSONDecoder().raw_decode(request.messages[-1].content)
                return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
                    "title": "Formula SAE Rules", "publisher": "SAE", "version": "1",
                    "tags": ["formula sae", "braking"], "published_at": "", "external_url": "",
                    "source_record_ids": body["actual_page_ids"][:1],
                    "warnings": ["Publication date is ambiguous."],
                })))

        class VisualExtractor:
            calls = 0

            async def extract(self, *args):
                self.calls += 1
                for page in details["pages"]:
                    page["visual_analyzed_at"] = "saved"

        async def on_response(*args):
            pass

        agent = DocumentIngestV1Agent(
            client=Client(), model=Model(), visual_extractor=VisualExtractor(),
            execution={"fencing_token": "current"}, on_response=on_response,
        )
        return agent, task, details, calls, requests

    async def test_ingestion_finishes_without_reading_notes_or_wiki_changes(self):
        agent, task, details, calls, requests = self.fixture()
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(result.output["outcome"], "ready")
        self.assertEqual(details["document"]["review_status"], "in_review")
        self.assertEqual(len(requests), 1)
        self.assertIsNone(details["notes"])
        self.assertFalse(details["changesets"])
        self.assertFalse(agent.definition.tool_ids)
        self.assertTrue(all(chunk["completed_at"] is None for chunk in details["chunks"]))
        self.assertEqual([path.rsplit("/", 1)[-1] for method, path, _ in calls if method == "POST"],
                         ["prepare", "metadata", "finish"])
        self.assertIn("Publication date is ambiguous.", result.output["warnings"])

    async def test_metadata_is_saved_and_editable_before_visual_analysis_finishes(self):
        agent, task, details, calls, _ = self.fixture()
        metadata_saved, visual_started = asyncio.Event(), asyncio.Event()
        original_request, original_extract = agent.client.request, agent.visual_extractor.extract
        original_complete = agent.model.complete

        async def extract(*args):
            visual_started.set()
            await metadata_saved.wait()
            await original_extract(*args)

        async def complete(request):
            await visual_started.wait()
            return await original_complete(request)

        async def request(url, method, path, **kwargs):
            response = await original_request(url, method, path, **kwargs)
            if path.endswith("/metadata"):
                self.assertTrue(all(not page.get("visual_analyzed_at") for page in details["pages"]))
                details["document"]["title"] = "User corrected early title"
                metadata_saved.set()
            return response

        agent.visual_extractor.extract, agent.model.complete, agent.client.request = extract, complete, request
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await asyncio.wait_for(agent.run(task), timeout=1)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertTrue(metadata_saved.is_set())
        self.assertEqual(details["document"]["title"], "User corrected early title")
        self.assertTrue(calls[-1][1].endswith("/finish"))

    async def test_visual_failure_cancels_and_awaits_metadata_task(self):
        agent, task, _, calls, _ = self.fixture()
        metadata_started, metadata_stopped = asyncio.Event(), asyncio.Event()

        async def complete(request):
            metadata_started.set()
            try:
                await asyncio.Event().wait()
            finally:
                metadata_stopped.set()

        async def extract(*args):
            await metadata_started.wait()
            raise KnowledgeClientError("knowledge_unavailable", "Visual analysis unavailable.", retryable=True)

        agent.model.complete, agent.visual_extractor.extract = complete, extract
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await asyncio.wait_for(agent.run(task), timeout=1)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertTrue(result.error.retryable)
        self.assertTrue(metadata_stopped.is_set())
        self.assertFalse(any(path.endswith("/metadata") or path.endswith("/finish") for _, path, _ in calls))

    async def test_cancelling_ingestion_stops_both_concurrent_tasks(self):
        agent, task, _, calls, _ = self.fixture()
        both_started = asyncio.Event()
        started, stopped = set(), set()

        async def wait(phase):
            started.add(phase)
            if len(started) == 2:
                both_started.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.add(phase)

        agent.model.complete = lambda request: wait("metadata")
        agent.visual_extractor.extract = lambda *args: wait("visuals")
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            execution = asyncio.create_task(agent.run(task))
            await asyncio.wait_for(both_started.wait(), timeout=1)
            execution.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await execution
        self.assertEqual(started, stopped)
        self.assertFalse(any(path.endswith("/finish") for _, path, _ in calls))

    async def test_metadata_failure_preserves_completed_visuals_for_retry(self):
        agent, task, details, calls, _ = self.fixture()
        visuals_done = asyncio.Event()
        original_extract, original_complete = agent.visual_extractor.extract, agent.model.complete

        async def extract(*args):
            await original_extract(*args)
            visuals_done.set()

        async def complete(request):
            await visuals_done.wait()
            raise KnowledgeClientError("knowledge_unavailable", "Metadata unavailable.", retryable=True)

        agent.visual_extractor.extract, agent.model.complete = extract, complete
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
            self.assertEqual(result.status, AgentResultStatus.FAILED)
            self.assertTrue(all(page["visual_analyzed_at"] for page in details["pages"]))
            self.assertFalse(any(path.endswith("/finish") for _, path, _ in calls))
            agent.model.complete = original_complete
            retried = await agent.run(task)
        self.assertEqual(retried.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(agent.visual_extractor.calls, 1)

    async def test_retry_reuses_extracted_metadata_and_user_overrides(self):
        agent, task, details, calls, requests = self.fixture()
        details["document"]["extracted_metadata"] = {"title": "Identified title", "warnings": []}
        details["document"]["title"] = "User corrected title"
        details["document"]["tags"] = ["team reference"]
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertFalse(requests)
        self.assertFalse(any(path.endswith("/metadata") for _, path, _ in calls))
        self.assertEqual(details["document"]["title"], "User corrected title")
        self.assertEqual(details["document"]["tags"], ["team reference"])

    async def test_lost_finish_response_is_retryable_without_repeating_model_work(self):
        agent, task, details, _, requests = self.fixture(fail_after_finish=True)
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
            self.assertEqual(result.status, AgentResultStatus.FAILED)
            self.assertTrue(result.error.retryable)
            retried = await agent.run(task)
        self.assertEqual(retried.output["outcome"], "ready")
        self.assertEqual(len(requests), 1)
        self.assertEqual(agent.visual_extractor.calls, 1)
        self.assertEqual(details["document"]["status"], "ready")

    async def test_missing_visual_extractor_fails_before_spawning_metadata_work(self):
        agent, task, _, _, requests = self.fixture()
        agent.visual_extractor = None
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}), self.assertRaisesRegex(RuntimeError, "not configured"):
            await agent.run(task)
        self.assertFalse(requests)

    async def test_early_metadata_cannot_cite_pages_outside_its_supplied_evidence(self):
        agent, task, details, _, requests = self.fixture()
        unexamined_source = new_ulid("sourcerecord")
        details["pages"].insert(1, {"page_number": 2, "source_record_id": unexamined_source})
        original_complete = agent.model.complete

        async def complete(request):
            response = await original_complete(request)
            metadata = json.loads(response.message.content)
            metadata["source_record_ids"] = [unexamined_source]
            return response.model_copy(update={"message": ModelMessage(
                role=ModelMessageRole.ASSISTANT, content=json.dumps(metadata),
            )})

        agent.model.complete = complete
        with self.assertRaisesRegex(ValueError, "valid result after three attempts"):
            await agent.extract_metadata(task, details, "http://knowledge", "documents/test")
        self.assertEqual(len(requests), 3)
        self.assertNotIn(unexamined_source, json.loads(requests[0].messages[-1].content)["actual_page_ids"])

    def test_metadata_validation_accepts_unknowns_and_rejects_invented_date_formats(self):
        metadata = {"title": "", "publisher": "", "version": "", "tags": [], "published_at": "",
                    "external_url": "", "source_record_ids": [], "warnings": ["Metadata is not established."]}
        ExtractedDocumentMetadata.model_validate(metadata)
        for value in ["2026-02-30", "2026-9-1", "unknown"]:
            with self.assertRaises(ValidationError):
                ExtractedDocumentMetadata.model_validate({**metadata, "published_at": value})

    def test_metadata_tags_are_free_form_bounded_and_grounded(self):
        metadata = {"title": "", "publisher": "", "version": "", "tags": [], "published_at": "",
                    "external_url": "", "source_record_ids": [new_ulid("sourcerecord")], "warnings": []}
        tagged = ExtractedDocumentMetadata.model_validate({**metadata, "tags": [" Driverless ", "AUTONOMY", "driverless"]})
        self.assertEqual(tagged.tags, ("driverless", "autonomy"))
        for tags in [[""], ["  "], ["two\nlines"], ["nul\x00"], ["é" * 33], [str(i) for i in range(9)]]:
            with self.assertRaises(ValidationError):
                ExtractedDocumentMetadata.model_validate({**metadata, "tags": tags})
        with self.assertRaises(ValidationError):
            ExtractedDocumentMetadata.model_validate({**metadata, "tags": ["autonomy"], "source_record_ids": []})
