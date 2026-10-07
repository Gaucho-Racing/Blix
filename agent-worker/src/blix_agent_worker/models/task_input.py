from typing import Annotated

from pydantic import ConfigDict, Field

from blix_agent_worker.models.common import ContractModel


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


class TaskQuestion(ContractModel):
    id: Annotated[str, Field(min_length=1, max_length=200)]
    questions: Annotated[list[QuestionItem], Field(min_length=1, max_length=10)]


class QuestionAnswer(ContractModel):
    model_config = ConfigDict(str_strip_whitespace=False)

    id: Annotated[str, Field(min_length=1, max_length=200)]
    selected_options: Annotated[list[str], Field(max_length=100)] = Field(default_factory=list)
    text: Annotated[str, Field(max_length=16000)] = ""


class TaskAnswer(ContractModel):
    question_id: Annotated[str, Field(min_length=1, max_length=200)]
    answers: Annotated[list[QuestionAnswer], Field(min_length=1, max_length=10)]

    def validate_question(self, question: TaskQuestion) -> None:
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


class TaskInputRequiredError(Exception):
    def __init__(self, question: TaskQuestion):
        super().__init__("Specialist is waiting for clarification from Blix.")
        self.question = question
