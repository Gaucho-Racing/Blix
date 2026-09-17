import json
import re
from typing import Annotated, Self

from pydantic import Field, HttpUrl, TypeAdapter, field_validator, model_validator

from blix_agent_worker.agents.deep_research.prompt import (
    DEEP_RESEARCH_V1_CONTEXT_SECTION,
    DEEP_RESEARCH_V1_SYSTEM_PROMPT,
    DEEP_RESEARCH_V1_USER_PROMPT,
)
from blix_agent_worker.agents.tool_agent import ToolAgent
from blix_agent_worker.models.agent import AgentDefinition, AgentKind, AgentTask
from blix_agent_worker.models.common import ContractModel, NonEmptyText
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole

_HTTP_URL_VALIDATOR = TypeAdapter(HttpUrl)


class DeepResearchV1Input(ContractModel):
    question: NonEmptyText


class DeepResearchSource(ContractModel):
    index: Annotated[int, Field(ge=1)]
    title: NonEmptyText
    url: NonEmptyText

    @field_validator("url")
    @classmethod
    def url_must_be_http(cls, value: str) -> str:
        _HTTP_URL_VALIDATOR.validate_python(value)
        return value


class DeepResearchV1Output(ContractModel):
    report: NonEmptyText
    sources: tuple[DeepResearchSource, ...]

    @model_validator(mode="after")
    def validate_source_index(self) -> Self:
        source_indices = [source.index for source in self.sources]
        if source_indices != list(range(1, len(self.sources) + 1)):
            raise ValueError("source indices must be sequential and start at 1")
        source_urls = [str(source.url) for source in self.sources]
        if len(source_urls) != len(set(source_urls)):
            raise ValueError("source URLs must be unique")
        citations = {int(value) for value in re.findall(r"\[(\d+)]", self.report)}
        if citations.difference(source_indices):
            raise ValueError("report contains citations that are missing from sources")
        if set(source_indices).difference(citations):
            raise ValueError("every indexed source must be cited in the report")
        return self


class DeepResearchV1Agent(ToolAgent):
    input_model = DeepResearchV1Input
    output_model = DeepResearchV1Output
    definition = AgentDefinition(
        id="deep_research_v1",
        kind=AgentKind.SPECIALIST,
        description=(
            "Research a question using public web and authorized internal sources, and return a rigorous "
            "report with inline source URLs."
        ),
        instructions=DEEP_RESEARCH_V1_SYSTEM_PROMPT,
        input_schema=DeepResearchV1Input.model_json_schema(mode="validation"),
        output_schema=DeepResearchV1Output.model_json_schema(mode="serialization"),
        model_profile="deep_research",
        tool_ids=(
            "current_time_v1",
            "discord_read_v1",
            "knowledge_read_v1",
            "sentinel_api_v1",
            "sentinel_impersonate_v1",
            "web_extract_v1",
            "web_search_v1",
        ),
    )

    def initial_messages(self, task: AgentTask, agent_input: ContractModel) -> list[ModelMessage]:
        prompt = DEEP_RESEARCH_V1_USER_PROMPT.format(objective=task.objective, **agent_input.model_dump())
        if task.context:
            prompt += DEEP_RESEARCH_V1_CONTEXT_SECTION.format(context=json.dumps(task.context, ensure_ascii=False))
        return [
            ModelMessage(role=ModelMessageRole.SYSTEM, content=self.definition.instructions),
            ModelMessage(role=ModelMessageRole.USER, content=prompt),
        ]
