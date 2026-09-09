from typing import Protocol

from blix_agent_worker.models.agent import AgentDefinition, AgentExecutionState, AgentResult, AgentRunOutcome, AgentTask


class Agent(Protocol):
    definition: AgentDefinition

    async def run(
        self,
        task: AgentTask,
        *,
        state: AgentExecutionState | None = None,
        resume_result: AgentResult | None = None,
    ) -> AgentRunOutcome: ...
