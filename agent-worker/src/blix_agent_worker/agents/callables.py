from collections.abc import Collection, Iterable
from types import MappingProxyType

from blix_agent_worker.models.agent import AgentDefinition
from blix_agent_worker.models.common import JsonObject
from blix_agent_worker.models.llm import ModelCallableDefinition, ModelCallableKind
from blix_agent_worker.models.tool import ToolDefinition


def restrict_callable_ids(declared: tuple[str, ...], restriction: Collection[str] | None) -> tuple[str, ...]:
    if restriction is None:
        return declared
    permitted = frozenset(restriction)
    return tuple(callable_id for callable_id in declared if callable_id in permitted)


class CallableCatalogError(Exception):
    pass


class DuplicateCallableError(CallableCatalogError):
    def __init__(self, callable_id: str) -> None:
        self.callable_id = callable_id
        super().__init__(f"callable {callable_id!r} is already registered")


class CallableNotFoundError(CallableCatalogError):
    def __init__(self, callable_id: str) -> None:
        self.callable_id = callable_id
        super().__init__(f"callable {callable_id!r} is not available")


class CallableCatalog:
    def __init__(
        self,
        *,
        tools: Iterable[ToolDefinition] = (),
        agents: Iterable[AgentDefinition] = (),
    ) -> None:
        definitions: dict[str, ModelCallableDefinition] = {}
        for tool in tools:
            self._add(
                definitions,
                ModelCallableDefinition(
                    id=tool.id,
                    kind=ModelCallableKind.TOOL,
                    description=tool.description,
                    input_schema=tool.input_schema,
                ),
            )
        for agent in agents:
            self._add(
                definitions,
                ModelCallableDefinition(
                    id=agent.id,
                    kind=ModelCallableKind.AGENT,
                    description=agent.description,
                    input_schema=self._agent_callable_schema(agent),
                ),
            )

        self._definitions_by_id = MappingProxyType(definitions)
        self.definitions = tuple(definitions[callable_id] for callable_id in sorted(definitions))

    def resolve_kind(self, callable_id: str) -> ModelCallableKind:
        try:
            return self._definitions_by_id[callable_id].kind
        except KeyError:
            raise CallableNotFoundError(callable_id) from None

    @staticmethod
    def _add(definitions: dict[str, ModelCallableDefinition], definition: ModelCallableDefinition) -> None:
        if definition.id in definitions:
            raise DuplicateCallableError(definition.id)
        definitions[definition.id] = definition

    @staticmethod
    def _agent_callable_schema(agent: AgentDefinition) -> JsonObject:
        agent_input_schema = agent.input_schema.copy()
        nested_definitions = agent_input_schema.pop("$defs", None)
        schema: JsonObject = {
            "type": "object",
            "properties": {
                "objective": {"type": "string", "minLength": 1},
                "input": agent_input_schema,
                "background": {
                    "type": "boolean",
                    "default": False,
                    "description": "Start the agent without waiting when its result is not needed for this response.",
                },
            },
            "required": ["objective", "input"],
            "additionalProperties": False,
        }
        if nested_definitions is not None:
            schema["$defs"] = nested_definitions
        return schema
