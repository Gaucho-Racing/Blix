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
                "kind": "rules", "published_at": "2026-09-01", "status": "reading",
            },
            "pages": [{"page_number": i + 1, "source_record_id": source} for i, source in enumerate(sources)],
            "chunks": [{"number": i + 1, "completed_at": "saved" if i == 0 and first_completed else None}
                       for i in range(2)],
            "notes": {"revision": 1, "notes": f"Earlier exception {sources[0]}"} if first_completed else None,
            "changesets": [],
        }
        calls = []

        class Client:
            async def request(self, url, method, path, *, payload=None):
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
                body = json.loads(request.messages[-1].content)
                if request.output_format.id == "document_metadata_v1":
                    return ModelResponse(message=ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
                        "title": "Formula SAE Rules", "publisher": "SAE", "version": "1",
                        "kind": "rules", "published_at": "", "external_url": "",
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
        self.assertTrue(all(call[2]["revision"] == i for i, call in enumerate(
            [call for call in calls if call[0] == "POST" and "/chunks/" in call[1]])))

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
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(len(requests), 1)
        self.assertFalse(any(path.endswith("/metadata") for _, path, _ in calls))
        self.assertEqual(curator_tasks[0].context["document"]["title"], "User corrected title")

    def test_metadata_validation_accepts_unknowns_and_rejects_invented_date_formats(self):
        metadata = {"title": "", "publisher": "", "version": "", "kind": "", "published_at": "",
                    "external_url": "", "source_record_ids": [], "warnings": ["Metadata is not established."]}
        ExtractedDocumentMetadata.model_validate(metadata)
        for value in ["2026-02-30", "2026-9-1", "unknown"]:
            with self.assertRaises(ValidationError):
                ExtractedDocumentMetadata.model_validate({**metadata, "published_at": value})

    async def test_completed_document_does_not_repeat_model_or_curation(self):
        agent, task, details, _, requests, curator_tasks = self.fixture(first_completed=True)
        details["document"]["status"] = "no_changes"
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertFalse(requests)
        self.assertFalse(curator_tasks)
