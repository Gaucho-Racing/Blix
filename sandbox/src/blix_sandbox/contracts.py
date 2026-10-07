from typing import Annotated

from pydantic import BaseModel, ConfigDict, Field


class ContractModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, str_strip_whitespace=True, validate_default=True)


NonEmptyText = Annotated[str, Field(min_length=1)]


class QuestionOption(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    label: Annotated[str, Field(min_length=1, max_length=500)]
    description: str | None = None
    preview: str | None = None


class QuestionItem(ContractModel):
    id: Annotated[str, Field(min_length=1, max_length=200)]
    question: Annotated[str, Field(min_length=1, max_length=8000)]
    options: Annotated[list[QuestionOption], Field(max_length=100)] = Field(default_factory=list)
    header: str | None = None
    multi: bool = False
    recommended: int | None = None


class SandboxQuestion(ContractModel):
    id: Annotated[str, Field(min_length=1, max_length=200)]
    questions: Annotated[list[QuestionItem], Field(min_length=1, max_length=10)]


class QuestionAnswer(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    id: Annotated[str, Field(min_length=1, max_length=200)]
    selected_options: Annotated[list[str], Field(max_length=100)] = Field(default_factory=list)
    text: Annotated[str, Field(max_length=16000)] = ""


class SandboxAnswer(ContractModel):
    question_id: Annotated[str, Field(min_length=1, max_length=200)]
    answers: Annotated[list[QuestionAnswer], Field(min_length=1, max_length=10)]

    def validate_question(self, question: SandboxQuestion) -> None:
        if self.question_id != question.id or len(self.answers) != len(question.questions):
            raise ValueError("Answer does not match the pending question.")
        for item, answer in zip(question.questions, self.answers, strict=True):
            labels = {option.label for option in item.options}
            choices = answer.selected_options
            if (
                answer.id != item.id
                or len(set(choices)) != len(choices)
                or not set(choices).issubset(labels)
                or (not item.multi and (len(choices) > 1 or (choices and answer.text.strip())))
                or (not choices and not answer.text.strip() and not item.multi)
            ):
                raise ValueError("Invalid answer for the pending question.")


class SandboxTraceEvent(ContractModel):
    sequence: Annotated[int, Field(ge=1)]
    timestamp: Annotated[str, Field(max_length=100)]
    kind: Annotated[str, Field(min_length=1, max_length=100)]
    content: Annotated[str, Field(max_length=8000)] = ""
    tool_name: Annotated[str, Field(max_length=200)] = ""
    tool_call_id: Annotated[str, Field(max_length=200)] = ""
    is_error: bool = False
    truncated: bool = False


class CodingInput(ContractModel):
    blix_run_url: Annotated[str, Field(pattern=r"^https?://[^\s<>]+$", max_length=2000)] | None = None
    requester_github_id: Annotated[int, Field(gt=0)] | None = None
    requester_name: Annotated[str, Field(min_length=1, max_length=200, pattern=r"^[^<>\r\n]+$")] | None = None
    requester_email: Annotated[str, Field(max_length=254, pattern=r"^[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+$")] | None = None
    requester_github_login: (
        Annotated[str, Field(pattern=r"^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$", max_length=39)] | None
    ) = None
    context: Annotated[str, Field(max_length=16000)] = ""


class SandboxRequest(CodingInput):
    objective: Annotated[str, Field(min_length=1, max_length=32000)]


class CodingOutput(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    summary: NonEmptyText
    base_sha: str = ""
    patch: str = ""
    changed_files: list[str] = Field(default_factory=list)
    pr_url: str | None = None
    commit_message: Annotated[str, Field(min_length=1, max_length=4000)] = "feat: implement coding task"
