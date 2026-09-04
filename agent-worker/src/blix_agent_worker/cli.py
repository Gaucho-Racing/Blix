import asyncio
import json
import os
from datetime import UTC, datetime

import typer
from pydantic import ValidationError
from rich.console import Console, Group
from rich.live import Live
from rich.markdown import Markdown
from rich.panel import Panel
from rich.spinner import Spinner
from rich.table import Table
from rich.text import Text
from rich.theme import Theme

from blix_agent_worker.agents.general_conversation import (
    GeneralConversationV1Agent,
    GeneralConversationV1Input,
    GeneralConversationV1Output,
)
from blix_agent_worker.llm import BifrostChatModel
from blix_agent_worker.models.agent import AgentResultStatus, AgentTask, AgentUsage
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool

BIFROST_BASE_URL = "https://bifrost.gauchoracing.com"
MODEL = "openai/gpt-5.6-luna"
MAX_CONVERSATION_MESSAGES = 40

THEME = Theme(
    {
        "accent": "#e105a3",
        "blix": "bold #e105a3",
        "command": "bold #e105a3",
        "error": "bold bright_red",
        "muted": "#9893a3",
        "success": "bold bright_green",
        "user": "bold #f3f1f6",
        "warning": "bold yellow",
    }
)
CONSOLE = Console(theme=THEME, highlight=False)
ERROR_CONSOLE = Console(theme=THEME, highlight=False, stderr=True)


def print_banner(agent_id: str, run_id: str, conversation_id: str) -> None:
    details = Table.grid(padding=(0, 2))
    details.add_column(style="muted", no_wrap=True)
    details.add_column(style="muted")
    details.add_row("AGENT", agent_id)
    details.add_row("MODEL", MODEL)
    details.add_row("RUN", f"run_{run_id}")
    details.add_row("CONVERSATION", f"conversation_{conversation_id}")
    details.add_row("COMMANDS", "/help  /tools  /reset  /quit")
    CONSOLE.print(
        Panel(
            details,
            title="[blix] BLIX [/blix]",
            subtitle="[muted]agent runtime[/muted]",
            border_style="accent",
            padding=(0, 2),
        )
    )


def print_help() -> None:
    commands = Table.grid(padding=(0, 2))
    commands.add_column(style="command", no_wrap=True)
    commands.add_column()
    commands.add_row("/help", "Show available commands")
    commands.add_row("/tools", "List tools available to this agent")
    commands.add_row("/reset", "Start a fresh conversation")
    commands.add_row("/quit", "End the session")
    CONSOLE.print(Panel(commands, title="[blix]Commands[/blix]", title_align="left", border_style="accent"))


def print_tools(registry: ToolRegistry, tool_ids: tuple[str, ...]) -> None:
    tools = Table(box=None, expand=True, show_header=True, header_style="muted", padding=(0, 2))
    tools.add_column("TOOL", style="command", no_wrap=True)
    tools.add_column("DESCRIPTION")
    for definition in registry.definitions(tool_ids):
        tools.add_row(definition.id, definition.description)
    CONSOLE.print(Panel(tools, title="[blix]Available tools[/blix]", title_align="left", border_style="accent"))


def print_error(message: str) -> None:
    ERROR_CONSOLE.print(
        Panel(Text(message, style="error"), title="[error]Error[/error]", title_align="left", border_style="red")
    )


class BlixTurnDisplay:
    def __init__(self, console: Console) -> None:
        self._console = console
        self._tool_events: list[Text] = []
        self._live: Live | None = None

    def start(self) -> None:
        self.stop()
        self._tool_events.clear()
        self._live = Live(
            self._pending_renderable(),
            console=self._console,
            refresh_per_second=12,
            transient=True,
        )
        self._live.start()

    def stop(self) -> None:
        if self._live is None:
            return
        live = self._live
        self._live = None
        live.stop()

    def record_tool_result(self, call: ToolCall, result: ToolResult) -> None:
        arguments = json.dumps(call.arguments, ensure_ascii=False, separators=(", ", ": "))
        event = Text("   ↳  tool  ", style="muted")
        event.append(call.tool_id, style="bold muted")
        if arguments != "{}":
            event.append(f"  {arguments}", style="muted")
        event.append(f"  ·  {result.status.value}", style="muted")
        self._tool_events.append(event)
        if self._live is not None:
            self._live.update(self._pending_renderable(), refresh=True)

    def print_response(self, message: str, usage: AgentUsage) -> None:
        self.stop()
        self._console.print()
        self._console.print(self._header(self._usage_summary(message, usage)))
        for event in self._tool_events:
            self._console.print(event)
        self._console.print(Panel(Markdown(message), border_style="muted", padding=(0, 1)))

    @staticmethod
    def _header(summary: str | None = None) -> Table:
        header = Table.grid(expand=True)
        header.add_column()
        header.add_column(style="muted", justify="right")
        header.add_row("[blix]●  blix[/blix]", summary or "")
        return header

    def _pending_renderable(self) -> Group:
        status = Table.grid(padding=(0, 1))
        status.add_column(width=2)
        status.add_column()
        status.add_row("", Spinner("dots", Text("thinking…", style="muted"), style="muted"))
        return Group(Text(""), self._header(), *self._tool_events, status)

    @staticmethod
    def _usage_summary(message: str, usage: AgentUsage) -> str:
        line_count = message.count("\n") + 1
        token_count = usage.input_tokens + usage.output_tokens
        line_label = "line" if line_count == 1 else "lines"
        model_call_label = "model call" if usage.model_calls == 1 else "model calls"
        tool_call_label = "tool call" if usage.tool_calls == 1 else "tool calls"
        return (
            f"{line_count} {line_label}  ·  {usage.model_calls} {model_call_label}  ·  "
            f"{usage.tool_calls} {tool_call_label}  ·  {token_count:,} tokens"
        )


async def run_cli() -> None:
    credentials = {
        "BIFROST_USERNAME": os.environ.get("BIFROST_USERNAME", "").strip(),
        "BIFROST_PASSWORD": os.environ.get("BIFROST_PASSWORD", ""),
        "BIFROST_VIRTUAL_KEY": os.environ.get("BIFROST_VIRTUAL_KEY", "").strip(),
    }
    missing_credentials = [name for name, value in credentials.items() if not value]
    if missing_credentials:
        raise RuntimeError(f"Missing required environment variables: {', '.join(missing_credentials)}")

    registry = ToolRegistry([CurrentTimeV1Tool()])
    tool_runner = ToolRunner(registry)
    turn_display = BlixTurnDisplay(CONSOLE)
    run_id = new_ulid()
    conversation_id = new_ulid()
    messages: list[ConversationMessage] = []

    async with BifrostChatModel(
        base_url=BIFROST_BASE_URL,
        username=credentials["BIFROST_USERNAME"],
        password=credentials["BIFROST_PASSWORD"],
        virtual_key=credentials["BIFROST_VIRTUAL_KEY"],
        model=MODEL,
    ) as model:
        agent = GeneralConversationV1Agent(
            model=model,
            tool_runner=tool_runner,
            on_tool_result=turn_display.record_tool_result,
        )
        print_banner(agent.definition.id, run_id, conversation_id)

        while True:
            try:
                user_input = CONSOLE.input("[user]●  you[/user] [muted]›[/muted] ").strip()
            except EOFError:
                CONSOLE.print()
                return

            if not user_input:
                continue
            if user_input == "/quit":
                CONSOLE.print("[muted]Session ended.[/muted]")
                return
            if user_input == "/help":
                print_help()
                continue
            if user_input == "/reset":
                run_id = new_ulid()
                conversation_id = new_ulid()
                messages.clear()
                CONSOLE.print("[success]Conversation reset.[/success]")
                print_banner(agent.definition.id, run_id, conversation_id)
                continue
            if user_input == "/tools":
                print_tools(registry, agent.definition.tool_ids)
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
                run_id=run_id,
                invocation_id=new_ulid(),
                agent_id=agent.definition.id,
                objective="Respond to the latest user message.",
                input=agent_input.model_dump(mode="json"),
                allowed_tool_ids=agent.definition.tool_ids,
            )

            turn_display.start()
            try:
                async with asyncio.timeout(task.budget.timeout_seconds):
                    result = await agent.run(task)
            except TimeoutError:
                turn_display.stop()
                print_error("The agent timed out.")
                continue
            finally:
                turn_display.stop()

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
            turn_display.print_response(output.message, result.usage)


def start_session() -> None:
    try:
        asyncio.run(run_cli())
    except KeyboardInterrupt:
        CONSOLE.print("\n[muted]Session ended.[/muted]")
    except RuntimeError as error:
        print_error(str(error))
        raise typer.Exit(code=1) from error


def main() -> None:
    typer.run(start_session)
