import json
from typing import Annotated, Self

from pydantic import Field, HttpUrl, TypeAdapter, field_validator, model_validator

from blix_agent_worker.agents.citations import validate_citations, validate_sources_retrieved
from blix_agent_worker.agents.deep_research.prompt import (
    DEEP_RESEARCH_V1_CONTEXT_SECTION,
    DEEP_RESEARCH_V1_KNOWN_CONTEXT_SECTION,
    DEEP_RESEARCH_V1_PREFERRED_DOMAINS_SECTION,
    DEEP_RESEARCH_V1_SYSTEM_PROMPT,
    DEEP_RESEARCH_V1_USER_PROMPT,
)
from blix_agent_worker.agents.tool_agent import ToolAgent, retrieved_urls
from blix_agent_worker.models.agent import AgentDefinition, AgentKind, AgentTask
from blix_agent_worker.models.common import ContractModel, NonEmptyText
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole
from blix_agent_worker.tools.web_search.web_search_v1 import Domain

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)
_WEB_TOOL_IDS = frozenset({"web_search_v1", "web_extract_v1"})


class DeepResearchV1Input(ContractModel):
    question: NonEmptyText
    known_context: NonEmptyText | None = Field(
        default=None,
        description="Internal facts already established by the caller that the web research should build on or verify.",
    )
    preferred_domains: Annotated[tuple[Domain, ...], Field(max_length=20)] = Field(
        default=(),
        description="Domains to prioritize, such as official rule or vendor sites.",
    )


class DeepResearchSource(ContractModel):
    index: Annotated[int, Field(ge=1)]
    title: NonEmptyText
    url: NonEmptyText
    published_date: NonEmptyText | None

    @field_validator("url")
    @classmethod
    def url_must_be_http(cls, value: str) -> str:
        _HTTP_URL_VALIDATOR.validate_python(value)
        return value


class DeepResearchV1Output(ContractModel):
    report: NonEmptyText
    sources: tuple[DeepResearchSource, ...]
    limitations: tuple[NonEmptyText, ...]

    @model_validator(mode="after")
    def validate_source_index(self) -> Self:
        validate_citations(self.report, [(source.index, source.url) for source in self.sources])
        return self


class DeepResearchV1Agent(ToolAgent):
    input_model = DeepResearchV1Input
    output_model = DeepResearchV1Output
    max_tool_calls = 40
    definition = AgentDefinition(
        id="deep_research_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Research a question on the public web and return a cited report. Use for external information such as "
            "competition rules, standards, datasheets, vendor and product details, technical references, and current "
            "events. It cannot read Blix's memory, team documents, Discord, or Sentinel, so pass any internal facts it "
            "needs in known_context."
        ),
        instructions=DEEP_RESEARCH_V1_SYSTEM_PROMPT,
        input_schema=DeepResearchV1Input.model_json_schema(mode="validation"),
        output_schema=DeepResearchV1Output.model_json_schema(mode="serialization"),
        model_profile="research",
        tool_ids=(
            "agent_ask_v1",
            "current_time_v1",
            "web_extract_v1",
            "web_search_v1",
        ),
    )

    def initial_messages(self, task: AgentTask, agent_input: DeepResearchV1Input) -> list[ModelMessage]:
        prompt = DEEP_RESEARCH_V1_USER_PROMPT.format(objective=task.objective, question=agent_input.question)
        if agent_input.known_context:
            prompt += DEEP_RESEARCH_V1_KNOWN_CONTEXT_SECTION.format(known_context=agent_input.known_context)
        if agent_input.preferred_domains:
            prompt += DEEP_RESEARCH_V1_PREFERRED_DOMAINS_SECTION.format(
                domains=", ".join(agent_input.preferred_domains)
            )
        if task.context:
            prompt += DEEP_RESEARCH_V1_CONTEXT_SECTION.format(context=json.dumps(task.context, ensure_ascii=False))
        return [
            ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
            ModelMessage(role=ModelMessageRole.USER, content=prompt),
        ]

    def validate_output(self, task: AgentTask, output: DeepResearchV1Output, messages: list[ModelMessage]) -> None:
        validate_sources_retrieved(
            [source.url for source in output.sources],
            retrieved_urls(messages, _WEB_TOOL_IDS),
            "web_search_v1 or web_extract_v1",
        )
