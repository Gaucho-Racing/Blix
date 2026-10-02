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
from blix_agent_worker.models.agent import AgentResult, AgentResultStatus, AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import ModelCallRequest, ModelMessage, ModelMessageRole, ModelResponse
from blix_agent_worker.models.tool import ToolResult, ToolResultStatus


class DocumentIngestionTests(unittest.IsolatedAsyncioTestCase):
    def test_document_curator_requires_coverage_and_original_reads_for_cited_pages(self):
        document_id = new_ulid("document")
        sources = [new_ulid("sourcerecord"), new_ulid("sourcerecord")]
        changeset_id = new_ulid("changeset")
        task = AgentTask(
            conversation_id=new_ulid("conversation"), session_id=new_ulid("session"),
            agent_id="knowledge_curator_v1", objective="Curate the relevant requirement.",
            input={"document_id": document_id, "source_record_ids": sources},
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
                    conversation_id=task.conversation_id, parent_session_id=task.session_id,
                    call_id=new_ulid("call"), status=ToolResultStatus.SUCCEEDED, output=output,
                ).model_dump_json(),
            ))

        manifest = {"document": {"document_id": document_id},
                    "pages": [{"source_record_id": source} for source in sources],
                    "chunks": [{"completed_at": "saved"}]}
        receipt("knowledge_document_read_v1", {"document_id": document_id}, manifest)
        receipt("knowledge_propose_v1", {}, {
            "changeset": {"changeset_id": changeset_id, "conversation_id": task.conversation_id,
                          "session_id": task.session_id, "status": "proposed"},
            "files": [{"path": "reference.md"}], "source_record_ids": [sources[0]],
        })
        output = KnowledgeCuratorV1Output(
            summary="Proposed a cited reference.", outcome="proposed", source_record_ids=tuple(sources),
            changesets=({"changeset_id": changeset_id, "status": "proposed", "commit_sha": "",
                        "paths": ("reference.md",)},), warnings=(),
        )
        curator = KnowledgeCuratorV1Agent.__new__(KnowledgeCuratorV1Agent)
        with self.assertRaisesRegex(ValueError, "read its source records"):
            curator.validate_output(task, output, messages)
        receipt("knowledge_evidence_read_v1", {"source_record_id": sources[0]}, {"source_record_id": sources[0]})
        curator.validate_output(task, output, messages)
        incomplete = copy.deepcopy(messages)
        result = ToolResult.model_validate_json(incomplete[1].content)
        result.output["chunks"][0]["completed_at"] = None
        incomplete[1] = incomplete[1].model_copy(update={"content": result.model_dump_json()})
        with self.assertRaisesRegex(ValueError, "complete document coverage"):
            curator.validate_output(task, output, incomplete)

    def test_completion_notification_can_start_a_lead_turn_without_prior_chat(self):
        BlixV1Input(messages=(), notifications=({"task_id": "task", "status": "succeeded"},))
        with self.assertRaises(ValidationError):
            BlixV1Input(messages=(), notifications=())

    def fixture(self, *, first_completed=False, fail_after_save=False):
        document_id = new_ulid("document")
        sources = [new_ulid("sourcerecord"), new_ulid("sourcerecord")]
        task = AgentTask(
            conversation_id=new_ulid("conversation"), session_id=new_ulid("session"),
            parent_session_id=new_ulid("session"), agent_id="document_ingest_v1",
            objective="Preserve rule exceptions and distinguish roadmap guidance.",
            input={"document_id": document_id, "generation": 1},
        )
        details = {
            "document": {
                "document_id": document_id, "title": "Official rules", "publisher": "SAE", "version": "1",
                "tags": ["formula sae", "braking"], "published_at": "2026-09-01", "status": "reading",
            },
            "pages": [{"page_number": i + 1, "source_record_id": source} for i, source in enumerate(sources)],
            "chunks": [{"number": i + 1, "completed_at": "saved" if i == 0 and first_completed else None}
                       for i in range(2)],
            "notes": {"revision": 1, "notes": f"Earlier exception {sources[0]}"} if first_completed else None,
            "changesets": [],
        }
        calls = []

        class Client:
            async def request(self, url, method, path, *, payload=None, params=None):
                calls.append((method, path, payload))
                if path.endswith("/prepare") and fail_after_save:
                    details["document"]["status"] = "reading"
                if method == "GET" and "/chunks/" in path:
                    number = int(path.rsplit("/", 1)[-1])
                    return {"chunk": {"number": number, "first_page": number, "last_page": number},
                            "evidence": [{"page": number, "source_record_id": sources[number - 1],
                                          "content": "Rule T.3.1 requires independent circuits; retain the exception."}]}
                if method == "POST" and "/chunks/" in path:
                    number = int(path.rsplit("/", 1)[-1])
                    details["chunks"][number - 1]["completed_at"] = "saved"
                    details["notes"] = {"revision": number, "notes": payload["notes"]}
                    if fail_after_save:
                        raise KnowledgeClientError("knowledge_unavailable", "Response was lost.", retryable=True)
                if path.endswith("/metadata"):
                    details["document"].update(payload["metadata"])
                    details["document"]["extracted_metadata"] = payload["metadata"]
                if path.endswith("/finish"):
                    details["document"]["status"] = "no_changes"
                return copy.deepcopy(details)

        requests = []

        class Model:
            async def prepare(self, request):
                return request

            async def complete(self, request):
                requests.append(request)
                body, _ = json.JSONDecoder().raw_decode(request.messages[-1].content)
                if request.output_format.id == "document_metadata_v1":
                    return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
                        "title": "Formula SAE Rules", "publisher": "SAE", "version": "1",
                        "tags": ["formula sae", "braking"], "published_at": "", "external_url": "",
                        "source_record_ids": body["actual_page_ids"][:1],
                        "warnings": ["Publication date is ambiguous."],
                    })))
                source = body["evidence"][0]["source_record_id"]
                return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
                    "findings": f"Verified requirement and exception {source}",
                    "notes": body["previous_notes"] + f"\nNew requirement {source}",
                    "source_record_ids": [source],
                })))

        curator_tasks = []

        class Curator:
            async def run(self, curator_task, **kwargs):
                curator_tasks.append(curator_task)
                return AgentResult(
                    conversation_id=task.conversation_id, session_id=task.session_id,
                    parent_session_id=task.parent_session_id, agent_id="knowledge_curator_v1",
                    status=AgentResultStatus.SUCCEEDED,
                    output={"outcome": "no_changes", "summary": "Existing wiki already covers these rules.",
                            "changesets": [], "warnings": []},
                )

        async def on_response(*args):
            pass

        agent = DocumentIngestV1Agent(
            client=Client(), model=Model(), curator=Curator(), execution={"fencing_token": "current"},
            on_response=on_response,
        )
        return agent, task, details, calls, requests, curator_tasks

    async def test_chunk_findings_update_notes_and_curator_proposes_only(self):
        agent, task, details, calls, requests, curator_tasks = self.fixture()
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(result.agent_id, "document_ingest_v1")
        self.assertEqual(len(requests), 3)
        second = json.loads(requests[1].messages[-1].content)
        self.assertIn(details["pages"][0]["source_record_id"], second["previous_notes"])
        self.assertEqual(curator_tasks[0].input["mode"], "propose")
        self.assertEqual(curator_tasks[0].input["document_id"], task.input["document_id"])
        self.assertIn("Publication date is ambiguous.", result.output["warnings"])
        self.assertEqual(curator_tasks[0].context["document"]["title"], "Formula SAE Rules")
        self.assertEqual(requests[-1].output_format.id, "document_metadata_v1")
        self.assertEqual(details["document"]["published_at"], "")
        self.assertEqual(details["document"]["tags"], ["formula sae", "braking"])
        self.assertEqual(curator_tasks[0].context["document"]["tags"], ["formula sae", "braking"])
        self.assertTrue(all(call[2]["revision"] == i for i, call in enumerate(
            [call for call in calls if call[0] == "POST" and "/chunks/" in call[1]])))

    async def test_metadata_is_saved_and_editable_before_reading_finishes(self):
        agent, task, details, calls, _, curator_tasks = self.fixture()
        metadata_saved = asyncio.Event()
        reading_started = asyncio.Event()
        original_complete = agent.model.complete
        original_request = agent.client.request

        async def complete(request):
            if request.output_format.id == "document_chunk_reading_v1":
                reading_started.set()
                await metadata_saved.wait()
            else:
                await reading_started.wait()
            return await original_complete(request)

        async def request(url, method, path, **kwargs):
            response = await original_request(url, method, path, **kwargs)
            if path.endswith("/metadata"):
                self.assertTrue(all(chunk["completed_at"] is None for chunk in details["chunks"]))
                details["document"]["title"] = "User corrected early title"
                metadata_saved.set()
            return response

        agent.model.complete = complete
        agent.client.request = request
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await asyncio.wait_for(agent.run(task), timeout=1)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertTrue(metadata_saved.is_set())
        self.assertEqual(curator_tasks[0].context["document"]["title"], "User corrected early title")
        metadata_index = next(i for i, call in enumerate(calls) if call[1].endswith("/metadata"))
        first_save = next(i for i, call in enumerate(calls) if call[0] == "POST" and "/chunks/" in call[1])
        self.assertLess(metadata_index, first_save)

    async def test_reading_failure_cancels_and_awaits_metadata_task(self):
        agent, task, _, calls, _, curator_tasks = self.fixture(fail_after_save=True)
        metadata_started = asyncio.Event()
        metadata_stopped = asyncio.Event()
        original_complete = agent.model.complete

        async def complete(request):
            if request.output_format.id == "document_metadata_v1":
                metadata_started.set()
                try:
                    await asyncio.Event().wait()
                finally:
                    metadata_stopped.set()
            await metadata_started.wait()
            return await original_complete(request)

        agent.model.complete = complete
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await asyncio.wait_for(agent.run(task), timeout=1)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertTrue(metadata_stopped.is_set())
        self.assertFalse(any(path.endswith("/metadata") for _, path, _ in calls))
        self.assertFalse(curator_tasks)

    async def test_cancelling_ingestion_stops_both_concurrent_model_calls(self):
        agent, task, _, calls, _, curator_tasks = self.fixture()
        both_started = asyncio.Event()
        started = set()
        stopped = set()

        async def complete(request):
            phase = request.output_format.id
            started.add(phase)
            if len(started) == 2:
                both_started.set()
            try:
                await asyncio.Event().wait()
            finally:
                stopped.add(phase)

        agent.model.complete = complete
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            execution = asyncio.create_task(agent.run(task))
            await asyncio.wait_for(both_started.wait(), timeout=1)
            execution.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await execution
        self.assertEqual(started, stopped)
        self.assertFalse(curator_tasks)
        self.assertFalse(any(path.endswith("/metadata") for _, path, _ in calls))

    async def test_early_metadata_cannot_cite_pages_outside_its_supplied_evidence(self):
        agent, task, details, _, requests, _ = self.fixture()
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

    async def test_metadata_failure_preserves_completed_reading_for_retry(self):
        agent, task, details, _, _, curator_tasks = self.fixture()
        original_complete = agent.model.complete

        async def complete(request):
            if request.output_format.id == "document_metadata_v1":
                raise KnowledgeClientError("knowledge_unavailable", "Metadata unavailable.", retryable=True)
            await asyncio.sleep(0)
            return await original_complete(request)

        agent.model.complete = complete
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertTrue(result.error.retryable)
        self.assertTrue(all(chunk["completed_at"] is not None for chunk in details["chunks"]))
        self.assertFalse(curator_tasks)

    async def test_resume_skips_completed_chunk_and_reuses_saved_notebook(self):
        agent, task, _, calls, requests, _ = self.fixture(first_completed=True)
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(len(requests), 2)
        self.assertIn("Earlier exception", json.loads(requests[0].messages[-1].content)["previous_notes"])
        self.assertFalse(any(path.endswith("/chunks/1") and method == "POST" for method, path, _ in calls))

    async def test_lost_save_response_is_retryable_and_preserves_progress(self):
        agent, task, details, _, _, curator_tasks = self.fixture(fail_after_save=True)
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertTrue(result.error.retryable)
        self.assertEqual(details["notes"]["revision"], 1)
        self.assertIsNotNone(details["chunks"][0]["completed_at"])
        self.assertFalse(curator_tasks)

    async def test_retry_reuses_extracted_metadata_and_user_overrides(self):
        agent, task, details, calls, requests, curator_tasks = self.fixture(first_completed=True)
        details["document"]["extracted_metadata"] = {"title": "Identified title", "warnings": []}
        details["document"]["title"] = "User corrected title"
        details["document"]["tags"] = ["team reference"]
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(len(requests), 1)
        self.assertFalse(any(path.endswith("/metadata") for _, path, _ in calls))
        self.assertEqual(curator_tasks[0].context["document"]["title"], "User corrected title")
        self.assertEqual(curator_tasks[0].context["document"]["tags"], ["team reference"])

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

    async def test_completed_document_does_not_repeat_model_or_curation(self):
        agent, task, details, _, requests, curator_tasks = self.fixture(first_completed=True)
        details["document"]["status"] = "no_changes"
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertFalse(requests)
        self.assertFalse(curator_tasks)
