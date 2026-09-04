import asyncio
import os
from datetime import UTC, datetime

import typer
from pydantic import ValidationError

from blix_agent_worker.agents.general_conversation import (
    GeneralConversationV1Agent,
    GeneralConversationV1Input,
    GeneralConversationV1Output,
)
from blix_agent_worker.llm import BifrostChatModel
from blix_agent_worker.models.agent import AgentResultStatus, AgentTask
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool

BIFROST_BASE_URL = "https://bifrost.gauchoracing.com"
MODEL = "openai/gpt-5.6-luna"
MAX_CONVERSATION_MESSAGES = 40


def print_error(message: str) -> None:
    typer.secho(f"error> {message}", fg=typer.colors.RED, bold=True, err=True)


async def run_cli() -> None:
    virtual_key = os.environ.get("BIFROST_VIRTUAL_KEY", "").strip()
    if not virtual_key:
        raise RuntimeError("BIFROST_VIRTUAL_KEY is required")

    registry = ToolRegistry([CurrentTimeV1Tool()])
    tool_runner = ToolRunner(registry)
    conversation_id = new_ulid()
    messages: list[ConversationMessage] = []

    async with BifrostChatModel(
        base_url=BIFROST_BASE_URL,
        virtual_key=virtual_key,
        model=MODEL,
    ) as model:
        agent = GeneralConversationV1Agent(model=model, tool_runner=tool_runner)
        typer.secho("Blix", fg=typer.colors.CYAN, bold=True)
        typer.echo(f"{agent.definition.id} · {MODEL}")
        typer.secho("Commands: /help, /reset, /tools, /quit", fg=typer.colors.BRIGHT_BLACK)

        while True:
            try:
                user_input = typer.prompt(
                    typer.style("you", fg=typer.colors.GREEN, bold=True),
                    prompt_suffix="> ",
                ).strip()
            except (EOFError, typer.Abort):
                typer.echo()
                return

            if not user_input:
                continue
            if user_input == "/quit":
                return
            if user_input == "/help":
                typer.echo("Enter a message, or use /reset, /tools, or /quit.")
                continue
            if user_input == "/reset":
                conversation_id = new_ulid()
                messages.clear()
                typer.secho("Conversation reset.", fg=typer.colors.YELLOW)
                continue
            if user_input == "/tools":
                for definition in registry.definitions(agent.definition.tool_ids):
                    typer.secho(definition.id, fg=typer.colors.CYAN, bold=True, nl=False)
                    typer.echo(f": {definition.description}")
                continue
            if user_input.startswith("/"):
                print_error(f"Unknown command: {user_input}")
                continue

            messages.append(
                ConversationMessage(
                    id=new_ulid(),
                    conversation_id=conversation_id,
                    role=ConversationMessageRole.USER,
                    content=user_input,
                    created_at=datetime.now(UTC),
                )
            )
            agent_input = GeneralConversationV1Input(
                conversation_id=conversation_id,
                messages=tuple(messages[-MAX_CONVERSATION_MESSAGES:]),
            )
            task = AgentTask(
                run_id=new_ulid(),
                invocation_id=new_ulid(),
                agent_id=agent.definition.id,
                objective="Respond to the latest user message.",
                input=agent_input.model_dump(mode="json"),
                allowed_tool_ids=agent.definition.tool_ids,
            )

            try:
                async with asyncio.timeout(task.budget.timeout_seconds):
                    result = await agent.run(task)
            except TimeoutError:
                print_error("The agent timed out.")
                continue

            if result.status is not AgentResultStatus.SUCCEEDED:
                message = result.error.message if result.error is not None else "The agent failed."
                print_error(message)
                continue

            try:
                output = GeneralConversationV1Output.model_validate(result.output)
            except ValidationError:
                print_error("The agent returned an invalid conversation response.")
                continue

            messages.append(
                ConversationMessage(
                    id=new_ulid(),
                    conversation_id=conversation_id,
                    role=ConversationMessageRole.ASSISTANT,
                    content=output.message,
                    created_at=datetime.now(UTC),
                )
            )
            typer.secho("blix", fg=typer.colors.CYAN, bold=True, nl=False)
            typer.echo(f"> {output.message}")


def start_session() -> None:
    try:
        asyncio.run(run_cli())
    except KeyboardInterrupt:
        typer.echo()
    except RuntimeError as error:
        typer.secho(f"error: {error}", fg=typer.colors.RED, bold=True, err=True)
        raise typer.Exit(code=1) from error


def main() -> None:
    typer.run(start_session)
