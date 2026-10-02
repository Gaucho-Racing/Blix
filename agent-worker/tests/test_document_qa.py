import json
import unittest
from datetime import UTC, datetime
from unittest.mock import patch

from blix_agent_worker.agents.document_qa.document_qa_v1 import DocumentQAV1Agent
from blix_agent_worker.clients.knowledge import KnowledgeClientError
from blix_agent_worker.models.agent import AgentResultStatus, AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import ModelCallRequest, ModelMessage, ModelMessageRole, ModelResponse
from blix_agent_worker.models.message import RunMessage
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.knowledge.knowledge_document_search_v1 import KnowledgeDocumentSearchV1Tool


class DocumentQATests(unittest.IsolatedAsyncioTestCase):
    def fixture(self, *, no_hits=False, foreign_evidence=False, search_fails=False):
        document_id = new_ulid("document")
        run_id, session_id = new_ulid("run"), new_ulid("session")
        citation = f"/knowledge/documents/{document_id}?preview=1&page=2"
        message = RunMessage(
            message_id=new_ulid("message"), run_id=run_id, session_id=session_id,
            role="user", content="What does T.3.2 require?", created_at=datetime.now(UTC),
        )
        task = AgentTask(
            run_id=run_id, session_id=session_id, agent_id="document_qa_v1",
            objective="Answer using this document.", input={"document_id": document_id, "messages": [message.model_dump(mode="json")]},
            allowed_tool_ids=("knowledge_document_search_v1",),
        )
        requests = []
        calls = []

        class Client:
            async def request(self, url, method, path, **kwargs):
                calls.append((path, kwargs["params"]))
                if search_fails:
                    raise KnowledgeClientError("knowledge_unavailable", "Search service unavailable.")
                evidence = {"document_id": new_ulid("document") if foreign_evidence else document_id,
                            "pdf_page": 2, "source_record_id": new_ulid("sourcerecord"),
                            "content": "T.3.2 requires two independent circuits.", "citation_url": citation,
                            "review_status": "pending"}
                return {"results": [] if no_hits else [evidence]} if kwargs["params"]["operation"] == "search" else evidence

        class Model:
            def __init__(self):
                self.messages = []

            async def complete(self, request):
                requests.append(request)
                return ModelResponse(message=self.messages.pop(0))

        model = Model()
        agent = DocumentQAV1Agent(model=model, tool_runner=ToolRunner(ToolRegistry([KnowledgeDocumentSearchV1Tool(Client())])))
        return agent, model, task, citation, requests, calls

    def tool_call(self, task, operation, **arguments):
        return ModelMessage(role=ModelMessageRole.ASSISTANT, call_requests=(ModelCallRequest(
            ref=new_ulid("call"), callable_id="knowledge_document_search_v1",
            arguments={"operation": operation, "scope": "run", "document_id": task.input["document_id"], **arguments},
        ),))

    def answer(self, text):
        return ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({"message": text}))

    async def test_answers_from_search_and_original_page_with_scoped_citations(self):
        agent, model, task, citation, requests, calls = self.fixture()
        model.messages = [self.tool_call(task, "search", query="T.3.2"),
                          self.tool_call(task, "read_page", page=2),
                          self.answer(f"This unapproved document requires two independent circuits. [PDF page 2]({citation})")]
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        self.assertEqual(result.usage.tool_calls, 2)
        self.assertTrue(all(set(item.id for item in request.callables) == {"knowledge_document_search_v1"} for request in requests))
        self.assertIn(task.input["document_id"], requests[0].messages[1].content)
        self.assertTrue(all(params["session_id"] == task.session_id and params["run_id"] == task.run_id for _, params in calls))
        self.assertIn(citation, result.output["message"])

    async def test_cannot_answer_without_searching(self):
        agent, model, task, _, _, _ = self.fixture()
        model.messages = [self.answer("Unverified remembered requirement.")]
        result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertEqual(result.error.code, "invalid_document_question")
        self.assertIn("current document", result.error.message)

    async def test_greeting_or_follow_up_is_rejected_with_actionable_validation_error(self):
        agent, model, task, _, _, calls = self.fixture()
        model.messages = [ModelMessage(role=ModelMessageRole.ASSISTANT, content=json.dumps({
            "message": "Ask a standalone question.", "response_kind": "invalid_question",
        }))]
        result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertIn("standalone question", result.error.message)
        self.assertEqual(result.error.code, "invalid_document_question")
        self.assertIn("greetings and follow-ups", result.error.message)
        self.assertFalse(calls)

    async def test_no_hits_can_produce_an_honest_no_answer(self):
        agent, model, task, _, _, _ = self.fixture(no_hits=True)
        model.messages = [self.tool_call(task, "search", query="T.3.2"), self.answer("I couldn’t establish that requirement from this document’s searchable text.")]
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)

    async def test_search_outage_is_not_reported_as_an_invalid_question(self):
        agent, model, task, _, _, _ = self.fixture(search_fails=True)
        model.messages = [self.tool_call(task, "search", query="brakes"), self.answer("Unable to search.")]
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await agent.run(task)
        self.assertEqual(result.status, AgentResultStatus.FAILED)
        self.assertEqual(result.error.code, "invalid_agent_output")
        self.assertIn("couldn’t search", result.error.message)

    async def test_rejects_invented_citations_and_foreign_document_evidence(self):
        for foreign in (False, True):
            agent, model, task, citation, _, _ = self.fixture(foreign_evidence=foreign)
            url = citation if foreign else "https://unverified.example/rules"
            model.messages = [self.tool_call(task, "search", query="brakes"), self.answer(f"Requirement. [Source]({url})")]
            with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
                result = await agent.run(task)
            self.assertEqual(result.status, AgentResultStatus.FAILED)
            self.assertEqual(result.error.code, "invalid_agent_output")

    async def test_wiki_write_or_web_search_is_not_an_available_callable(self):
        for tool_id in ("knowledge_apply_v1", "web_search_v1", "agent_execution_v1"):
            agent, model, task, _, _, calls = self.fixture()
            model.messages = [ModelMessage(role=ModelMessageRole.ASSISTANT, call_requests=(
                ModelCallRequest(ref="outside-scope", callable_id=tool_id, arguments={}),
            ))]
            result = await agent.run(task)
            self.assertEqual(result.status, AgentResultStatus.FAILED)
            self.assertEqual(result.error.code, "unknown_model_callable")
            self.assertFalse(calls)
