import json
from typing import Annotated, Self

from pydantic import Field, HttpUrl, TypeAdapter, field_validator, model_validator

from blix_agent_worker.agents.citations import validate_citations, validate_sources_retrieved
from blix_agent_worker.agents.discord_research.prompt import (
    DISCORD_RESEARCH_V1_CONTEXT_SECTION,
    DISCORD_RESEARCH_V1_KNOWN_CONTEXT_SECTION,
    DISCORD_RESEARCH_V1_SCOPE_SECTION,
    DISCORD_RESEARCH_V1_SYSTEM_PROMPT,
    DISCORD_RESEARCH_V1_USER_PROMPT,
)
from blix_agent_worker.agents.tool_agent import ToolAgent, retrieved_urls
from blix_agent_worker.models.agent import AgentDefinition, AgentKind, AgentTask
from blix_agent_worker.models.common import ContractModel, NonEmptyText
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)
_DISCORD_TOOL_IDS = frozenset({"discord_read_v1", "discord_search_v1"})


class DiscordResearchV1Input(ContractModel):
    question: NonEmptyText
    scope: NonEmptyText | None = Field(
        default=None,
        description="The slice to cover, such as specific channels or threads, a date range, or a topic.",
    )
    known_context: NonEmptyText | None = Field(
        default=None,
        description="Facts or Discord references the caller already has, such as channel, thread, or message IDs.",
    )


class DiscordResearchSource(ContractModel):
    index: Annotated[int, Field(ge=1)]
    title: NonEmptyText
    url: NonEmptyText

    @field_validator("url")
    @classmethod
    def url_must_be_http(cls, value: str) -> str:
        _HTTP_URL_VALIDATOR.validate_python(value)
        return value


class DiscordResearchV1Output(ContractModel):
    report: NonEmptyText
    sources: tuple[DiscordResearchSource, ...]
    limitations: tuple[NonEmptyText, ...]

    @model_validator(mode="after")
    def validate_source_index(self) -> Self:
        validate_citations(self.report, [(source.index, source.url) for source in self.sources])
        return self


class DiscordResearchV1Agent(ToolAgent):
    input_model = DiscordResearchV1Input
    output_model = DiscordResearchV1Output
    max_tool_calls = 40
    definition = AgentDefinition(
        id="discord_research_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Research a bounded slice of Gaucho Racing's Discord, such as a channel, thread, date range, or topic, and "
            "return a cited report of what was discussed and decided. Launch several in the background with "
            "non-overlapping scopes for broad history requests."
        ),
        instructions=DISCORD_RESEARCH_V1_SYSTEM_PROMPT,
        input_schema=DiscordResearchV1Input.model_json_schema(mode="validation"),
        output_schema=DiscordResearchV1Output.model_json_schema(mode="serialization"),
        model_profile="research",
        tool_ids=(
            "agent_ask_v1",
            "current_time_v1",
            "discord_read_v1",
            "discord_search_v1",
        ),
    )

    def initial_messages(self, task: AgentTask, agent_input: DiscordResearchV1Input) -> list[ModelMessage]:
        prompt = DISCORD_RESEARCH_V1_USER_PROMPT.format(objective=task.objective, question=agent_input.question)
        if agent_input.scope:
            prompt += DISCORD_RESEARCH_V1_SCOPE_SECTION.format(scope=agent_input.scope)
        if agent_input.known_context:
            prompt += DISCORD_RESEARCH_V1_KNOWN_CONTEXT_SECTION.format(known_context=agent_input.known_context)
        if task.context:
            prompt += DISCORD_RESEARCH_V1_CONTEXT_SECTION.format(context=json.dumps(task.context, ensure_ascii=False))
        return [
            ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
            ModelMessage(role=ModelMessageRole.USER, content=prompt),
        ]

    def validate_output(self, task: AgentTask, output: DiscordResearchV1Output, messages: list[ModelMessage]) -> None:
        validate_sources_retrieved(
            [source.url for source in output.sources],
            retrieved_urls(messages, _DISCORD_TOOL_IDS),
            "discord_read_v1 or discord_search_v1",
        )
