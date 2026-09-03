import asyncio
import json
from collections.abc import Sequence
from typing import Any

from pydantic import ValidationError

from blix_agent_runtime.agents import AgentDefinition, default_agents
from blix_agent_runtime.errors import AgentRuntimeError, ToolConfigurationError
from blix_agent_runtime.gateway import ModelGateway, ToolCall
from blix_agent_runtime.tools import DelegateTaskInput, ToolRegistry, create_default_tool_registry, delegate_tool_schema


class AgentRuntime:
    def __init__(
        self,
        *,
        gateway: ModelGateway,
        agents: Sequence[AgentDefinition] | None = None,
        tools: ToolRegistry | None = None,
        primary_agent: str = "blix",
        max_tool_rounds: int = 8,
    ) -> None:
        configured_agents = agents or default_agents()
        self._agents = {agent.name: agent for agent in configured_agents}
        if primary_agent not in self._agents:
            raise ToolConfigurationError(f"Primary agent {primary_agent!r} is not configured")
        if max_tool_rounds < 1:
            raise ToolConfigurationError("max_tool_rounds must be at least 1")

        self.gateway = gateway
        self.tools = tools or create_default_tool_registry()
        self.primary_agent = primary_agent
        self.max_tool_rounds = max_tool_rounds
        self._history: list[dict[str, Any]] = []
        self.last_usage: dict[str, Any] = {}

    async def chat(self, user_input: str) -> str:
        if not user_input.strip():
            raise AgentRuntimeError("User input cannot be empty")

        self._history.append({"role": "user", "content": user_input})
        response = await self._run_agent(self._agents[self.primary_agent], self._history)
        return response

    def reset(self) -> None:
        self._history.clear()
        self.last_usage = {}

    def agent_summaries(self) -> tuple[tuple[str, str], ...]:
        return tuple((agent.name, agent.description) for agent in self._agents.values())

    async def _run_agent(self, agent: AgentDefinition, messages: list[dict[str, Any]]) -> str:
        conversation = [{"role": "system", "content": agent.instructions}, *messages]

        for _ in range(self.max_tool_rounds):
            model_tools = self.tools.schemas(agent.allowed_tools)
            if agent.can_delegate:
                model_tools.append(delegate_tool_schema(self._subagent_names()))

            turn = await self.gateway.complete(messages=conversation, tools=model_tools)
            self.last_usage = turn.usage
            conversation.append(turn.message)

            if not turn.tool_calls:
                if not turn.text:
                    raise AgentRuntimeError("Model returned neither text nor tool calls")
                if agent.name == self.primary_agent:
                    self._history[:] = conversation[1:]
                return turn.text

            tool_results = await asyncio.gather(*(self._execute_tool(agent, call) for call in turn.tool_calls))
            conversation.extend(tool_results)

        raise AgentRuntimeError(f"Agent exceeded the maximum of {self.max_tool_rounds} tool rounds")

    async def _execute_tool(self, agent: AgentDefinition, tool_call: ToolCall) -> dict[str, Any]:
        try:
            arguments = json.loads(tool_call.arguments)
        except json.JSONDecodeError as error:
            result: dict[str, Any] = {
                "ok": False,
                "error": {"type": "invalid_json", "message": str(error)},
            }
        else:
            if not isinstance(arguments, dict):
                result = {
                    "ok": False,
                    "error": {"type": "invalid_arguments", "message": "Tool arguments must be a JSON object"},
                }
            elif tool_call.name == "delegate_task":
                result = await self._delegate(agent, arguments)
            elif agent.allowed_tools is not None and tool_call.name not in agent.allowed_tools:
                result = {
                    "ok": False,
                    "error": {"type": "tool_not_allowed", "message": f"Tool {tool_call.name!r} is not allowed"},
                }
            else:
                result = await self.tools.execute(tool_call.name, arguments)

        return {
            "role": "tool",
            "tool_call_id": tool_call.id,
            "name": tool_call.name,
            "content": json.dumps(result, ensure_ascii=False),
        }

    async def _delegate(self, caller: AgentDefinition, arguments: dict[str, Any]) -> dict[str, Any]:
        if not caller.can_delegate:
            return {
                "ok": False,
                "error": {"type": "delegation_not_allowed", "message": f"Agent {caller.name!r} cannot delegate"},
            }

        try:
            request = DelegateTaskInput.model_validate(arguments)
        except ValidationError as error:
            return {
                "ok": False,
                "error": {
                    "type": "invalid_arguments",
                    "message": "Delegation arguments did not match the required schema",
                    "details": error.errors(include_url=False),
                },
            }

        subagent = self._agents.get(request.agent)
        if subagent is None or subagent.name == self.primary_agent:
            return {
                "ok": False,
                "error": {"type": "unknown_agent", "message": f"Unknown subagent: {request.agent}"},
            }

        content = request.task
        if request.context:
            content = f"Task:\n{request.task}\n\nContext:\n{request.context}"

        try:
            response = await self._run_agent(subagent, [{"role": "user", "content": content}])
        except AgentRuntimeError as error:
            return {"ok": False, "error": {"type": "subagent_failure", "message": str(error)}}
        return {"ok": True, "value": {"agent": subagent.name, "response": response}}

    def _subagent_names(self) -> tuple[str, ...]:
        return tuple(name for name in self._agents if name != self.primary_agent)
