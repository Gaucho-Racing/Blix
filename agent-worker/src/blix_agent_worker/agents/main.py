from typing import Protocol

from blix_agent_worker.models.agent import AgentDefinition, AgentResult, AgentTask


class Agent(Protocol):
    definition: AgentDefinition

    async def run(self, task: AgentTask) -> AgentResult: ...
