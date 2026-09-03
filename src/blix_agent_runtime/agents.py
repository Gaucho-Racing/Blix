from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class AgentDefinition:
    name: str
    description: str
    instructions: str
    allowed_tools: frozenset[str] | None = None
    can_delegate: bool = False


def default_agents() -> tuple[AgentDefinition, ...]:
    return (
        AgentDefinition(
            name="blix",
            description="The primary Gaucho Racing assistant.",
            instructions=(
                "You are Blix, an internal Gaucho Racing assistant. Be direct, technically rigorous, and explicit when "
                "information is uncertain. Use tools when they provide better evidence than memory. Delegate focused "
                "analysis when it materially improves the answer, and return a cohesive final response to the user."
            ),
            can_delegate=True,
        ),
        AgentDefinition(
            name="analyst",
            description=(
                "Performs focused technical analysis and identifies assumptions, trade-offs, and failure modes."
            ),
            instructions=(
                "Perform the delegated analysis only. State assumptions, compare realistic alternatives, identify "
                "failure modes, and give the parent agent a concise recommendation."
            ),
            allowed_tools=frozenset({"calculate", "current_time"}),
        ),
    )
