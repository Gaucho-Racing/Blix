from collections.abc import Collection, Iterable
from types import MappingProxyType

from blix_agent_worker.agents.main import Agent
from blix_agent_worker.models.agent import AgentDefinition


class AgentRegistryError(Exception):
    pass


class DuplicateAgentError(AgentRegistryError):
    def __init__(self, agent_id: str) -> None:
        self.agent_id = agent_id
        super().__init__(f"agent {agent_id!r} is already registered")


class AgentNotFoundError(AgentRegistryError):
    def __init__(self, agent_id: str) -> None:
        self.agent_id = agent_id
        super().__init__(f"agent {agent_id!r} is not registered")


class AgentRegistry:
    def __init__(self, agents: Iterable[Agent] = ()) -> None:
        registered: dict[str, Agent] = {}
        for agent in agents:
            agent_id = agent.definition.id
            if agent_id in registered:
                raise DuplicateAgentError(agent_id)
            registered[agent_id] = agent

        self._agents = MappingProxyType(registered)
        self._definitions = tuple(registered[agent_id].definition for agent_id in sorted(registered))
        self._ids = frozenset(registered)

    def __len__(self) -> int:
        return len(self._agents)

    def definitions(self, allowed_ids: Collection[str] | None = None) -> tuple[AgentDefinition, ...]:
        if allowed_ids is None:
            return self._definitions

        allowed_ids = frozenset(allowed_ids)
        unknown_ids = allowed_ids.difference(self._ids)
        if unknown_ids:
            raise AgentNotFoundError(min(unknown_ids))

        return tuple(definition for definition in self._definitions if definition.id in allowed_ids)

    def resolve(self, agent_id: str) -> Agent:
        try:
            return self._agents[agent_id]
        except KeyError:
            raise AgentNotFoundError(agent_id) from None
