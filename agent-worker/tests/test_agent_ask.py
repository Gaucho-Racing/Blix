import asyncio
import json
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock

from blix_agent_worker.agents.deep_research import DeepResearchV1Agent
from blix_agent_worker.agents.knowledge_curator import KnowledgeCuratorV1Agent
from blix_agent_worker.models.agent import AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.llm import ModelCallRequest, ModelMessage, ModelResponse
from blix_agent_worker.models.task_input import TaskInputRequiredError
from blix_agent_worker.models.tool import ToolCall, ToolDefinition, ToolImpact, ToolResultStatus
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.agent_ask import AgentAskV1Tool


class AgentAskTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.lease = SimpleNamespace(post=AsyncMock(return_value={"commands": []}))
        self.acknowledge = Mock()
        self.tool = AgentAskV1Tool(self.lease, self.acknowledge)
        self.call = ToolCall(
            run_id=new_ulid("run"),
            call_id=new_ulid("call"),
            parent_session_id=new_ulid("session"),
            tool_id="agent_ask_v1",
            idempotency_key="clarify-scope",
            arguments={"questions": [{"id": "scope", "question": "Which scope?", "options": [{"label": "Small"}]}]},
        )

    async def test_question_is_stable_across_attempts_and_does_not_acknowledge_unanswered_input(self):
        ids = []
        for call_id in (self.call.call_id, new_ulid("call")):
            with self.assertRaises(TaskInputRequiredError) as raised:
                await ToolRunner(ToolRegistry([self.tool])).execute(self.call.model_copy(update={"call_id": call_id}))
            ids.append(raised.exception.question.id)
        self.assertEqual(ids[0], ids[1])
        self.acknowledge.assert_not_called()

    async def test_cancel_is_honored_while_waiting(self):
        self.lease.post.return_value = {"commands": [{"kind": "cancel"}]}
        with self.assertRaises(asyncio.CancelledError):
            await self.tool.execute(self.call)

    async def test_invalid_questions_return_a_tool_failure(self):
        result = await self.tool.execute(self.call.model_copy(update={"arguments": {"questions": []}}))
        self.assertEqual(result.status, ToolResultStatus.FAILED)
        self.lease.post.assert_not_awaited()

    async def test_both_specialists_resume_the_pending_tool_with_the_actual_answer(self):
        source_id = new_ulid("sourcerecord")
        for agent_class, agent_input, output in (
            (
                DeepResearchV1Agent,
                {"question": "Research this"},
                {"report": "Findings for the selected scope", "sources": []},
            ),
            (
                KnowledgeCuratorV1Agent,
                {"source_record_ids": [source_id]},
                {
                    "summary": "Need source access",
                    "outcome": "blocked",
                    "source_record_ids": [source_id],
                    "changesets": [],
                    "warnings": [],
                },
            ),
        ):
            with self.subTest(agent=agent_class.definition.id):
                self.lease.post.return_value = {"commands": []}
                task = AgentTask(
                    run_id=self.call.run_id,
                    session_id=self.call.parent_session_id,
                    agent_id=agent_class.definition.id,
                    objective="Investigate",
                    input=agent_input,
                )
                tools = [self.tool]
                for tool_id in agent_class.definition.tool_ids:
                    if tool_id != "agent_ask_v1":
                        tools.append(
                            SimpleNamespace(
                                definition=ToolDefinition(
                                    id=tool_id,
                                    description="Fixture",
                                    input_schema={"type": "object"},
                                    impact=ToolImpact.READ_ONLY,
                                )
                            )
                        )
                checkpoints = []

                async def checkpoint(*arguments):
                    checkpoints.append(arguments[-1])

                model = SimpleNamespace(
                    complete=AsyncMock(
                        return_value=ModelResponse(
                            message=ModelMessage(
                                role="assistant",
                                call_requests=(
                                    ModelCallRequest(
                                        ref=self.call.idempotency_key,
                                        callable_id="agent_ask_v1",
                                        arguments=self.call.arguments,
                                    ),
                                ),
                            )
                        )
                    )
                )
                agent = agent_class(
                    model=model, tool_runner=ToolRunner(ToolRegistry(tools)), on_model_response=checkpoint
                )
                with self.assertRaises(TaskInputRequiredError) as raised:
                    await agent.run(task)
                answer = {
                    "question_id": raised.exception.question.id,
                    "answers": [{"id": "scope", "selected_options": ["Small"], "text": ""}],
                }
                self.lease.post.return_value = {
                    "commands": [
                        {"command_id": "stale", "kind": "answer", "payload": {"question_id": "old"}},
                        {"command_id": "answer_1", "kind": "answer", "payload": answer},
                    ]
                }
                resumed_model = SimpleNamespace(
                    complete=AsyncMock(
                        return_value=ModelResponse(
                            message=ModelMessage(role="assistant", content=json.dumps(output)),
                        )
                    )
                )
                resumed = agent_class(
                    model=resumed_model, tool_runner=ToolRunner(ToolRegistry(tools)), on_tool_result=checkpoint
                )
                result = await resumed.run(task, state=checkpoints[0])
                self.assertEqual(result.status, "succeeded")
                self.assertEqual(result.session_id, task.session_id)
                request = resumed_model.complete.call_args.args[0]
                responses = [
                    json.loads(message.content) for message in request.messages if message.role == "call_output"
                ]
                self.assertEqual(responses[-1]["output"], answer)
                self.acknowledge.assert_called_with("answer_1")
