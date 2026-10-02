import asyncio
import json
import unittest
from datetime import UTC, datetime, timedelta
from unittest.mock import patch

import httpx
from pydantic import ValidationError

from blix_agent_worker.agents import AgentRegistry, AgentRunner
from blix_agent_worker.agents.blix import BlixV1Agent, BlixV1Input
from blix_agent_worker.agents.deep_research import DeepResearchV1Agent
from blix_agent_worker.clients.runway import Attempt, AttemptLease, RunwayClient
from blix_agent_worker.models.agent import (
    AgentExecutionState,
    AgentResult,
    AgentResultStatus,
    AgentSuspension,
    AgentTask,
)
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.context import CallerContext
from blix_agent_worker.models.llm import ModelCallRequest, ModelMessage, ModelMessageRole, ModelResponse
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult, ToolResultStatus
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool
from blix_agent_worker.tools.knowledge import KnowledgeRememberV1Tool
from blix_agent_worker.worker import ApprovalRequiredError, RuntimeModel


class ScriptedModel:
    def __init__(self, *messages):
        self.messages = iter(messages)
        self.requests = []

    async def complete(self, request, **kwargs):
        self.requests.append(request)
        return ModelResponse(message=next(self.messages))


def assistant(content=None, calls=()):
    return ModelMessage(role=ModelMessageRole.ASSISTANT, content=content, call_requests=calls)


def lead_task():
    conversation, session = new_ulid("conversation"), new_ulid("session")
    message = ConversationMessage(
        message_id=new_ulid("message"),
        conversation_id=conversation,
        session_id=session,
        role="user",
        content="Help with research",
        created_at=datetime.now(UTC),
        event_position=1,
    )
    return AgentTask(
        conversation_id=conversation,
        session_id=session,
        agent_id="blix_v1",
        objective="Help the user",
        input=BlixV1Input(messages=(message,)).model_dump(mode="json"),
        allowed_tool_ids=("current_time_v1",),
        allowed_subagent_ids=("deep_research_v1",),
    )


def make_lead(model, tools=None, **kwargs):
    tools = tools or ToolRunner(ToolRegistry([CurrentTimeV1Tool()]))
    research = DeepResearchV1Agent(model=model, tool_runner=tools)
    return BlixV1Agent(model=model, tool_runner=tools, agent_runner=AgentRunner(AgentRegistry([research])), **kwargs)


class RuntimeTests(unittest.IsolatedAsyncioTestCase):
    async def test_quick_answer_uses_one_model_call(self):
        model = ScriptedModel(assistant("A quick answer"))
        result = await make_lead(model).run(lead_task())
        self.assertEqual(result.output, {"message": "A quick answer"})
        self.assertEqual(result.usage.model_calls, 1)
        self.assertEqual(result.usage.delegation_calls, 0)

    async def test_delayed_reply_preserves_current_question_and_delivery_order(self):
        task = lead_task()
        question = ConversationMessage.model_validate(task.input["messages"][0])
        delayed = question.model_copy(
            update={
                "message_id": new_ulid("message"),
                "role": ConversationMessageRole.ASSISTANT,
                "content": "Earlier research",
                "event_position": 2,
            }
        )
        task = task.model_copy(
            update={
                "input": BlixV1Input(messages=(question, delayed), reply_to_message_id=question.message_id).model_dump(
                    mode="json"
                )
            }
        )
        model = ScriptedModel(assistant("Current answer"))
        result = await make_lead(model).run(task)
        messages = model.requests[0].messages
        self.assertIn(str(question.message_id), messages[0].content)
        self.assertIn(question.content, messages[1].content)
        self.assertEqual(messages[2].content, delayed.content)
        self.assertEqual(messages[3].role, ModelMessageRole.USER)
        self.assertIn(str(question.message_id), messages[3].content)
        self.assertEqual(result.output, {"message": "Current answer"})

    def test_reply_target_must_identify_an_existing_user_message(self):
        question = ConversationMessage.model_validate(lead_task().input["messages"][0])
        with self.assertRaises(ValidationError):
            BlixV1Input(messages=(question,), reply_to_message_id=new_ulid("message"))
        reply = question.model_copy(update={"role": ConversationMessageRole.ASSISTANT})
        with self.assertRaises(ValidationError):
            BlixV1Input(messages=(reply,), reply_to_message_id=reply.message_id)

    async def test_tool_checkpoint_resumes_without_repeating_tool(self):
        model = ScriptedModel(
            assistant(calls=(ModelCallRequest(ref="time-ref", callable_id="current_time_v1", arguments={}),)),
            assistant("Done"),
        )
        checkpoints = []

        async def checkpoint(call, result, state):
            checkpoints.append(state)

        task = lead_task()
        result = await make_lead(model, on_tool_result=checkpoint).run(task)
        self.assertEqual(result.status, AgentResultStatus.SUCCEEDED)
        resumed_model = ScriptedModel(assistant("Resumed"))
        resumed = await make_lead(resumed_model).run(
            task, state=AgentExecutionState.model_validate_json(checkpoints[0].model_dump_json())
        )
        self.assertEqual(resumed.output, {"message": "Resumed"})
        self.assertEqual(resumed.usage.tool_calls, 1)
        self.assertEqual(len(resumed_model.requests), 1)

    async def test_blocking_delegation_roundtrip(self):
        call = ModelCallRequest(
            ref="research-ref",
            callable_id="deep_research_v1",
            arguments={"objective": "Find evidence", "input": {"question": "What happened?"}},
        )
        task = lead_task()
        suspended = await make_lead(ScriptedModel(assistant(calls=(call,)))).run(task)
        self.assertIsInstance(suspended, AgentSuspension)
        child = suspended.child_task
        result = AgentResult(
            conversation_id=child.conversation_id,
            session_id=child.session_id,
            parent_session_id=task.session_id,
            agent_id=child.agent_id,
            status="succeeded",
            output={"report": "Evidence", "sources": []},
        )
        model = ScriptedModel(assistant("Research answer"))
        resumed = await make_lead(model).run(task, state=suspended.state, resume_result=result)
        self.assertEqual(resumed.output["message"], "Research answer")
        self.assertEqual(model.requests[0].messages[-1].call_request_ref, "research-ref")

    async def test_background_delegation_returns_task_id_without_waiting(self):
        task_id = new_ulid("task")
        delegated = []

        async def background(task, ref):
            delegated.append((task, ref))
            return task_id

        call = ModelCallRequest(
            ref="background-ref",
            callable_id="deep_research_v1",
            arguments={"objective": "Find evidence", "input": {"question": "What happened?"}, "background": True},
        )
        model = ScriptedModel(assistant(calls=(call,)), assistant("Started"))
        result = await make_lead(model, on_background_agent=background).run(lead_task())
        self.assertEqual(result.output["message"], "Started")
        self.assertEqual(len(delegated), 1)
        self.assertIn(task_id, model.requests[1].messages[-1].content)

    async def test_steering_survives_tool_and_model_checkpoints(self):
        command = {"command_id": new_ulid("command"), "kind": "steer", "payload": {"content": "Focus on the budget"}}

        class Lease:
            async def post(self, operation):
                return {"commands": [command]}

        base = ScriptedModel(
            assistant(calls=(ModelCallRequest(ref="time-ref", callable_id="current_time_v1", arguments={}),)),
            assistant("Done"),
        )
        runtime = RuntimeModel(base, Lease(), (command,))
        checkpoints = []

        async def checkpoint(call, result, state):
            checkpoints.append(state)

        await make_lead(runtime, on_before_model=runtime.prepare, on_tool_result=checkpoint).run(lead_task())
        for messages in (checkpoints[0].messages, base.requests[1].messages):
            self.assertEqual(sum("Focus on the budget" in (m.content or "") for m in messages), 1)
        self.assertEqual(runtime.acknowledged, [command["command_id"]])

    async def test_approval_suspends_before_tool_side_effect(self):
        task = lead_task()
        checkpoints = []

        async def authorize(call, definition):
            raise ApprovalRequiredError(new_ulid("action"))

        async def checkpoint(sequence, response, state):
            checkpoints.append(state)

        tools = ToolRunner(ToolRegistry([CurrentTimeV1Tool()]), authorize=authorize)
        model = ScriptedModel(
            assistant(calls=(ModelCallRequest(ref="time-ref", callable_id="current_time_v1", arguments={}),))
        )
        with self.assertRaises(ApprovalRequiredError):
            await make_lead(model, tools, on_model_response=checkpoint).run(task)
        self.assertEqual(checkpoints[0].next_call_index, 0)
        approved = await make_lead(ScriptedModel(assistant("Approved"))).run(task, state=checkpoints[0])
        self.assertEqual(approved.output["message"], "Approved")

    async def test_completed_action_replay_skips_execution(self):
        call = ToolCall(
            conversation_id=new_ulid("conversation"),
            parent_session_id=new_ulid("session"),
            call_id=new_ulid("call"),
            tool_id="current_time_v1",
            arguments={},
        )
        replay = ToolResult(
            conversation_id=call.conversation_id,
            parent_session_id=call.parent_session_id,
            call_id=call.call_id,
            status="succeeded",
            output={"cached": True},
        )

        async def authorize(call, definition):
            return replay

        with patch.object(CurrentTimeV1Tool, "execute", side_effect=AssertionError("must not run")):
            result = await ToolRunner(ToolRegistry([CurrentTimeV1Tool()]), authorize=authorize).execute(call)
        self.assertEqual(result, replay)

    async def test_lease_serializes_versioned_writes_and_heartbeat(self):
        versions = []

        async def handler(request):
            payload = json.loads(request.content)
            versions.append(payload["expected_version"])
            await asyncio.sleep(0.001)
            return httpx.Response(200, json={"version": payload["expected_version"] + 1})

        attempt = Attempt(
            attempt_id=new_ulid("attempt"),
            conversation_id=new_ulid("conversation"),
            session_id=new_ulid("session"),
            target_id=new_ulid("turn"),
            version=1,
            fencing_token="fence",
            worker_id=new_ulid("worker"),
            deadline_at=datetime.now(UTC) + timedelta(minutes=1),
            runtime_kind="api",
        )
        async with RunwayClient("http://runway", transport=httpx.MockTransport(handler)) as client:
            lease = AttemptLease(client, attempt)
            await asyncio.gather(lease.post("heartbeat"), lease.post("checkpoints"), lease.post("events"))
        self.assertEqual(versions, [1, 2, 3])
        self.assertEqual(attempt.version, 4)

    async def test_chat_capture_uses_original_message_and_new_identity(self):
        task = lead_task()
        message = ConversationMessage.model_validate(task.input["messages"][0])
        payloads = []

        class Knowledge:
            async def request(self, url, method, path, *, payload):
                payloads.append(payload)
                return {
                    "record": {"source_id": new_ulid("source"), "source_record_id": new_ulid("sourcerecord")},
                    "created": True,
                }

        caller = CallerContext(entity_id=new_ulid("ent"), entity_type="USER", user_id=new_ulid("usr"))
        delayed = message.model_copy(
            update={
                "message_id": new_ulid("message"),
                "role": ConversationMessageRole.ASSISTANT,
                "content": "Earlier answer",
            }
        )
        tool = KnowledgeRememberV1Tool(Knowledge(), (message, delayed), caller)
        with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
            result = await tool.execute(
                ToolCall(
                    conversation_id=task.conversation_id,
                    parent_session_id=task.session_id,
                    call_id=new_ulid("call"),
                    tool_id=tool.definition.id,
                    arguments={},
                )
            )
        self.assertEqual(result.status, ToolResultStatus.SUCCEEDED)
        self.assertEqual(payloads[0]["content"], message.content)
        self.assertEqual(payloads[0]["session_id"], task.session_id)
        self.assertEqual(payloads[0]["entity_id"], caller.entity_id)


class ApiRuntimeTests(unittest.IsolatedAsyncioTestCase):
    def context(self, task=None):
        from blix_agent_worker.clients.runway import ExecutionContext

        lead = lead_task()
        session = task.session_id if task else lead.session_id
        profile = task.agent_id if task else "blix_v1"
        return ExecutionContext.model_validate(
            {
                "attempt": {
                    "attempt_id": new_ulid("attempt"),
                    "conversation_id": lead.conversation_id,
                    "session_id": session,
                    "target_id": new_ulid("task" if task else "turn"),
                    "version": 1,
                    "fencing_token": "fence",
                    "worker_id": new_ulid("worker"),
                    "deadline_at": (datetime.now(UTC) + timedelta(minutes=5)).isoformat(),
                    "runtime_kind": "api",
                },
                "conversation": {
                    "conversation_id": lead.conversation_id,
                    "caller_context": {"entity_id": new_ulid("ent"), "entity_type": "USER", "user_id": new_ulid("usr")},
                },
                "session": {"session_id": session, "profile_id": profile, "runtime_kind": "api", "memory": {}},
                "profile": {
                    "profile_id": profile,
                    "tool_ids": [],
                    "policy": {"model_profile": "research" if task else "lead"},
                },
                "turn": {"input_message_id": lead.input["messages"][0]["message_id"]} if not task else None,
                "task": {"invocation": task.model_dump(mode="json")} if task else None,
                "messages": [] if task else lead.input["messages"],
                "allowed_tool_ids": [],
                "lease_renew_after_ms": 1000,
            }
        )

    async def test_runtime_routes_model_and_answers_question_before_delayed_reply(self):
        from blix_agent_worker.clients.sentinel import SentinelUser
        from blix_agent_worker.worker import ApiRuntime

        execution = self.context()
        question = execution.messages[0]
        execution.messages = (
            question,
            question.model_copy(
                update={
                    "message_id": new_ulid("message"),
                    "role": ConversationMessageRole.ASSISTANT,
                    "content": "Earlier research",
                    "event_position": 2,
                }
            ),
        )
        caller = SentinelUser.model_construct(
            id=execution.conversation.caller_context.user_id,
            entity_id=execution.conversation.caller_context.entity_id,
            first_name="Test",
            last_name="User",
            username="test",
            avatar_url="",
            groups=("BlixUsers",),
        )

        class Runtime(ApiRuntime):
            async def resolve_caller(self, execution):
                return caller

        operations = []

        class Lease:
            async def post(self, operation, payload=None):
                operations.append((operation, payload))
                return {"commands": []}

            async def events(self, events):
                operations.append(("events", events))

            async def checkpoint(self, state, **kwargs):
                operations.append(("checkpoint", state))

        model = ScriptedModel(assistant("Quick reply"))
        model.model = "test-lead"
        await Runtime({"lead": model}, None, None, None, None, None).execute(execution, Lease())
        self.assertEqual(operations[-1][0], "complete")
        self.assertEqual(operations[-1][1]["content"], "Quick reply")
        self.assertEqual(len(model.requests), 1)
        self.assertIn(str(question.message_id), model.requests[0].messages[-1].content)
        self.assertTrue(any(op == "checkpoint" for op, _ in operations))

    async def test_concurrent_document_model_streams_keep_their_call_ids_and_text(self):
        from blix_agent_worker.agents.document_ingest.document_ingest_v1 import DocumentIngestV1Agent
        from blix_agent_worker.clients.sentinel import SentinelUser
        from blix_agent_worker.models.llm import ModelRequest
        from blix_agent_worker.worker import ApiRuntime

        task = lead_task().model_copy(update={"agent_id": "document_ingest_v1"})
        execution = self.context(task)
        caller = SentinelUser.model_construct(
            id=execution.conversation.caller_context.user_id,
            entity_id=execution.conversation.caller_context.entity_id,
            first_name="Test", last_name="User", username="test", avatar_url="", groups=("BlixUsers",),
        )

        class Runtime(ApiRuntime):
            async def resolve_caller(self, execution):
                return caller

        events = []

        class Lease:
            async def post(self, operation, payload=None):
                return {"commands": []}

            async def events(self, batch):
                events.extend(batch)
                await asyncio.sleep(0)

        both_started = asyncio.Event()

        class Model:
            model = "test-document"
            started = 0

            async def complete(self, request, *, on_text_delta):
                self.started += 1
                if self.started == 2:
                    both_started.set()
                await both_started.wait()
                await on_text_delta(str(request.sequence) * 600)
                await asyncio.sleep(0)
                await on_text_delta(str(request.sequence) * 180)
                return ModelResponse(message=assistant("Saved"))

        async def run(agent, invocation, **kwargs):
            async def complete(sequence):
                request = ModelRequest(sequence=sequence, messages=(assistant("Read"),))
                response = await agent.model.complete(await agent.model.prepare(request))
                await agent.on_response(sequence, response)
            await asyncio.gather(complete(1), complete(2))
            return AgentResult(
                conversation_id=invocation.conversation_id, session_id=invocation.session_id,
                agent_id=invocation.agent_id, status=AgentResultStatus.SUCCEEDED, output={},
            )

        with patch.object(DocumentIngestV1Agent, "run", run):
            await asyncio.wait_for(
                Runtime({"research": Model()}, None, None, None, None, None).execute(execution, Lease()),
                timeout=1,
            )
        calls = {event["entity_id"]: event["payload"]["sequence"]
                 for event in events if event["type"] == "model.call_v1"}
        self.assertEqual(len(calls), 2)
        for call_id, sequence in calls.items():
            streamed = "".join(event["payload"]["delta"] for event in events
                               if event["type"] == "model.output.delta_v1" and event["entity_id"] == call_id)
            self.assertEqual(streamed, str(sequence) * 780)

    async def test_document_qa_turn_uses_specialist_and_rechecks_access_before_completion(self):
        from blix_agent_worker.clients.knowledge import KnowledgeClientError
        from blix_agent_worker.clients.sentinel import SentinelUser
        from blix_agent_worker.worker import ApiRuntime

        for rejected in (False, True):
            execution = self.context()
            document_id = new_ulid("document")
            execution.conversation.document_ids = (document_id,)
            execution.profile.profile_id = "document_qa_v1"
            execution.session.profile_id = "document_qa_v1"
            execution.allowed_tool_ids = ("knowledge_document_search_v1",)
            caller = SentinelUser.model_construct(
                id=execution.conversation.caller_context.user_id,
                entity_id=execution.conversation.caller_context.entity_id,
                first_name="Test", last_name="Admin", username="test", avatar_url="", groups=("BlixAdmins",),
            )

            class Runtime(ApiRuntime):
                async def resolve_caller(self, execution):
                    return caller

            operations = []

            class Lease:
                async def post(self, operation, payload=None):
                    operations.append((operation, payload))
                    return {"action": {"action_id": new_ulid("action"), "status": "authorized"}, "commands": []}

                async def events(self, batch):
                    pass

                async def checkpoint(self, state, **kwargs):
                    pass

            access_checks = []

            class Knowledge:
                async def request(self, url, method, path, **kwargs):
                    if path == "documents/access":
                        access_checks.append(kwargs["payload"])
                        if rejected and len(access_checks) == 2:
                            raise KnowledgeClientError("knowledge_not_found", "Document was rejected.")
                        return {"allowed": True}
                    return {"results": []}

            model = ScriptedModel(
                assistant(calls=(ModelCallRequest(ref="search", callable_id="knowledge_document_search_v1",
                    arguments={"operation": "search", "scope": "conversation", "document_id": document_id, "query": "brakes"}),)),
                assistant(json.dumps({"message": "I couldn’t find that in this document."})),
            )
            model.model = "test-qa"
            with patch.dict("os.environ", {"KNOWLEDGE_URL": "http://knowledge"}):
                run = Runtime({"lead": model}, None, None, None, Knowledge(), None).execute(execution, Lease())
                if rejected:
                    with self.assertRaises(KnowledgeClientError):
                        await run
                else:
                    await run
            self.assertEqual(len(access_checks), 2)
            self.assertTrue(all(check == {"document_ids": [document_id], "mode": "questions"} for check in access_checks))
            self.assertTrue(all(request.output_format.id == "document_qa_v1" for request in model.requests))
            completed = [payload for operation, payload in operations if operation == "complete"]
            self.assertEqual(len(completed), 0 if rejected else 1)

    async def test_compaction_preserves_late_replies_before_queued_questions(self):
        from blix_agent_worker.worker import ApiRuntime

        execution = self.context()
        template = execution.messages[0]
        positions = (1, 2, 10, 3, 4, 5, 6, 7, 8, 9, 11, 12)
        execution.messages = tuple(
            template.model_copy(
                update={
                    "message_id": new_ulid("message"),
                    "event_position": position,
                    "content": "message " + str(position),
                }
            )
            for position in positions
        )
        updates = []

        class Lease:
            async def post(self, operation, payload):
                updates.append(payload["memory"])
                return {}

        model = ScriptedModel(assistant("Summary"))
        with patch.dict("os.environ", {"LEAD_HISTORY_MAX_CHARS": "1"}):
            retained = await ApiRuntime({}, None, None, None, None, None).compact_history(execution, Lease(), model)
        summarized = {m["content"] for m in json.loads(model.requests[0].messages[1].content)["messages"]}
        remaining = {m.content for m in retained}
        self.assertFalse(summarized & remaining)
        self.assertEqual(summarized | remaining, {m.content for m in execution.messages})
        self.assertIn("message 10", remaining)
        self.assertEqual(updates[0]["through_position"], 3)

    async def test_compaction_keeps_question_before_many_delayed_replies(self):
        from blix_agent_worker.worker import ApiRuntime

        execution = self.context()
        question = execution.messages[0].model_copy(update={"event_position": 3})
        template = question.model_copy(update={"message_id": new_ulid("message"), "event_position": 1})
        delayed = tuple(
            question.model_copy(
                update={
                    "message_id": new_ulid("message"),
                    "role": ConversationMessageRole.ASSISTANT,
                    "event_position": position,
                    "content": "Delayed answer " + str(position),
                }
            )
            for position in range(4, 14)
        )
        execution.messages = (template, question, *delayed)
        updates = []

        class Lease:
            async def post(self, operation, payload):
                updates.append(payload["memory"])
                return {}

        model = ScriptedModel(assistant("Summary"))
        with patch.dict("os.environ", {"LEAD_HISTORY_MAX_CHARS": "1"}):
            retained = await ApiRuntime({}, None, None, None, None, None).compact_history(execution, Lease(), model)
        self.assertEqual(retained, (question, *delayed))
        self.assertEqual(updates[0]["through_position"], 1)


class TaskControlTests(unittest.IsolatedAsyncioTestCase):
    async def test_steering_carries_stable_replay_key(self):
        from blix_agent_worker.tools.agent_execution import AgentExecutionV1Tool

        sent = []

        class Lease:
            async def post(self, operation, payload):
                sent.append(payload)
                return {"operation": "steer"}

        call = ToolCall(
            conversation_id=new_ulid("conversation"),
            parent_session_id=new_ulid("session"),
            call_id=new_ulid("call"),
            tool_id="agent_execution_v1",
            idempotency_key="stable-ref",
            arguments={"operation": "steer", "task_id": new_ulid("task"), "content": "Focus on costs"},
        )
        result = await AgentExecutionV1Tool(Lease()).execute(call)
        self.assertEqual(result.status, ToolResultStatus.SUCCEEDED)
        self.assertEqual(sent[0]["request_ref"], "stable-ref")
