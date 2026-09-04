import asyncio
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

from blix_agent_worker.agents import AgentRegistry, AgentRunner
from blix_agent_worker.agents.deep_research import DeepResearchV1Agent
from blix_agent_worker.agents.general_conversation import (
    GeneralConversationV1Agent,
    GeneralConversationV1Input,
    GeneralConversationV1Output,
)
from blix_agent_worker.clients import TavilyClient
from blix_agent_worker.llm import BifrostResponsesModel
from blix_agent_worker.models.agent import AgentResult, AgentResultStatus, AgentTask, AgentUsage
from blix_agent_worker.models.common import new_ulid
from blix_agent_worker.models.message import ConversationMessage, ConversationMessageRole
from blix_agent_worker.models.tool import ToolCall, ToolResult
from blix_agent_worker.tools import ToolRegistry, ToolRunner
from blix_agent_worker.tools.current_time import CurrentTimeV1Tool
from blix_agent_worker.tools.web_extract import WebExtractV1Tool
from blix_agent_worker.tools.web_search import WebSearchV1Tool

BIFROST_BASE_URL = "https://bifrost.gauchoracing.com"
MODEL = "openai/gpt-5.6-luna"

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


def print_banner(agent_id: str, run_id: str, execution_id: str) -> None:
    details = Table.grid(padding=(0, 2))
    details.add_column(style="muted", no_wrap=True)
    details.add_column(style="muted")
    details.add_row("AGENT", agent_id)
    details.add_row("MODEL", MODEL)
    details.add_row("RUN", f"run_{run_id}")
    details.add_row("EXECUTION", f"execution_{execution_id}")
    details.add_row("COMMANDS", "/help  /agents  /tools  /reset  /quit")
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
    commands.add_row("/agents", "List agents available for delegation")
    commands.add_row("/tools", "List tools available to this agent")
    commands.add_row("/reset", "Start a fresh run")
    commands.add_row("/quit", "End the session")
    CONSOLE.print(Panel(commands, title="[blix]Commands[/blix]", title_align="left", border_style="accent"))


def print_tools(registry: ToolRegistry, tool_ids: tuple[str, ...]) -> None:
    tools = Table(box=None, expand=True, show_header=True, header_style="muted", padding=(0, 2))
    tools.add_column("TOOL", style="command", no_wrap=True)
    tools.add_column("DESCRIPTION")
    for definition in registry.definitions(tool_ids):
        tools.add_row(definition.id, definition.description)
    CONSOLE.print(Panel(tools, title="[blix]Available tools[/blix]", title_align="left", border_style="accent"))


def print_agents(registry: AgentRegistry, agent_ids: tuple[str, ...]) -> None:
    agents = Table(box=None, expand=True, show_header=True, header_style="muted", padding=(0, 2))
    agents.add_column("AGENT", style="command", no_wrap=True)
    agents.add_column("DESCRIPTION")
    for definition in registry.definitions(agent_ids):
        agents.add_row(definition.id, definition.description)
    CONSOLE.print(Panel(agents, title="[blix]Available agents[/blix]", title_align="left", border_style="accent"))


def print_error(message: str) -> None:
    ERROR_CONSOLE.print(
        Panel(Text(message, style="error"), title="[error]Error[/error]", title_align="left", border_style="red")
    )


class BlixTurnDisplay:
    def __init__(self, console: Console) -> None:
        self._console = console
        self._activity_events: list[Text] = []
        self._agent_event_indexes: dict[str, int] = {}
        self._live: Live | None = None

    def start(self) -> None:
        self.stop()
        self._activity_events.clear()
        self._agent_event_indexes.clear()
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
        self._record_tool_result(call, result, prefix="   ↳  tool  ")

    def record_subagent_tool_result(self, call: ToolCall, result: ToolResult) -> None:
        self._record_tool_result(call, result, prefix="      ↳  tool  ")

    def _record_tool_result(self, call: ToolCall, result: ToolResult, *, prefix: str) -> None:
        event = Text(prefix, style="muted")
        event.append(call.tool_id, style="bold muted")
        event.append(f"  ·  {result.status.value}", style="muted")
        self._activity_events.append(event)
        self._refresh()

    def record_agent_start(self, task: AgentTask) -> None:
        event = Text("   ↳  agent  ", style="muted")
        event.append(task.agent_id, style="bold muted")
        event.append("  ·  running", style="muted")
        self._agent_event_indexes[task.execution_id] = len(self._activity_events)
        self._activity_events.append(event)
        self._refresh()

    def record_agent_result(self, result: AgentResult) -> None:
        event = Text("   ↳  agent  ", style="muted")
        event.append(result.agent_id, style="bold muted")
        event.append(f"  ·  {result.status.value}", style="muted")
        if result.usage.model_calls or result.usage.tool_calls:
            model_call_label = "model call" if result.usage.model_calls == 1 else "model calls"
            tool_call_label = "tool call" if result.usage.tool_calls == 1 else "tool calls"
            event.append(
                f"  ·  {result.usage.model_calls} {model_call_label}  ·  "
                f"{result.usage.tool_calls} {tool_call_label}",
                style="muted",
            )
        index = self._agent_event_indexes.get(result.execution_id)
        if index is None:
            self._activity_events.append(event)
        else:
            self._activity_events[index] = event
        self._refresh()

    def _refresh(self) -> None:
        if self._live is not None:
            self._live.update(self._pending_renderable(), refresh=True)

    def print_response(self, message: str, usage: AgentUsage) -> None:
        self.stop()
        self._console.print()
        self._console.print(self._header(self._usage_summary(message, usage)))
        for event in self._activity_events:
            self._console.print(event)
        self._console.print(Panel(Markdown(message), border_style="muted", padding=(0, 1)))

    @staticmethod
    def _header(summary: str | None = None) -> Table:
        header = Table.grid(expand=True, padding=(0, 1))
        header.add_column(no_wrap=True)
        header.add_column(style="muted", justify="right")
        header.add_row("[blix]●  blix[/blix]", summary or "")
        return header

    def _pending_renderable(self) -> Group:
        status = Table.grid(padding=(0, 1))
        status.add_column(width=2)
        status.add_column()
        status.add_row("", Spinner("dots", Text("thinking…", style="muted"), style="muted"))
        return Group(Text(""), self._header(), *self._activity_events, status)

    @staticmethod
    def _usage_summary(message: str, usage: AgentUsage) -> str:
        line_count = message.count("\n") + 1
        token_count = usage.input_tokens + usage.output_tokens
        line_label = "line" if line_count == 1 else "lines"
        model_call_label = "model call" if usage.model_calls == 1 else "model calls"
        tool_label = "tool" if usage.tool_calls == 1 else "tools"
        agent_label = "agent" if usage.delegation_calls == 1 else "agents"
        return (
            f"{line_count} {line_label}  ·  {usage.model_calls} {model_call_label}  ·  "
            f"{usage.tool_calls} {tool_label}  ·  {usage.delegation_calls} {agent_label}  ·  "
            f"{token_count:,} tokens"
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

    turn_display = BlixTurnDisplay(CONSOLE)
    run_id = new_ulid()
    execution_id = new_ulid()
    messages: list[ConversationMessage] = []

    async with (
        BifrostResponsesModel(
            base_url=BIFROST_BASE_URL,
            username=credentials["BIFROST_USERNAME"],
            password=credentials["BIFROST_PASSWORD"],
            virtual_key=credentials["BIFROST_VIRTUAL_KEY"],
            model=MODEL,
        ) as model,
        TavilyClient() as tavily_client,
    ):
        registry = ToolRegistry(
            [
                CurrentTimeV1Tool(),
                WebExtractV1Tool(tavily_client),
                WebSearchV1Tool(tavily_client),
            ]
        )
        tool_runner = ToolRunner(registry)
        deep_research_agent = DeepResearchV1Agent(
            model=model,
            tool_runner=tool_runner,
            on_tool_result=turn_display.record_subagent_tool_result,
        )
        agent_registry = AgentRegistry([deep_research_agent])
        agent_runner = AgentRunner(
            agent_registry,
            on_start=turn_display.record_agent_start,
            on_result=turn_display.record_agent_result,
        )
        agent = GeneralConversationV1Agent(
            model=model,
            tool_runner=tool_runner,
            agent_runner=agent_runner,
            on_tool_result=turn_display.record_tool_result,
        )
        print_banner(agent.definition.id, run_id, execution_id)

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
            if user_input == "/agents":
                print_agents(agent_registry, agent.definition.subagent_ids)
                continue
            if user_input == "/reset":
                run_id = new_ulid()
                execution_id = new_ulid()
                messages.clear()
                CONSOLE.print("[success]Run reset.[/success]")
                print_banner(agent.definition.id, run_id, execution_id)
                continue
            if user_input == "/tools":
                print_tools(registry, agent.definition.tool_ids)
                continue
            if user_input.startswith("/"):
                print_error(f"Unknown command: {user_input}")
                continue

            messages.append(
                ConversationMessage(
                    message_id=new_ulid(),
                    run_id=run_id,
                    parent_execution_id=execution_id,
                    role=ConversationMessageRole.USER,
                    content=user_input,
                    created_at=datetime.now(UTC),
                )
            )
            agent_input = GeneralConversationV1Input(
                messages=tuple(messages),
            )
            task = AgentTask(
                run_id=run_id,
                execution_id=execution_id,
                agent_id=agent.definition.id,
                objective="Respond to the latest user message.",
                input=agent_input.model_dump(mode="json"),
                allowed_tool_ids=agent.definition.tool_ids,
                allowed_subagent_ids=agent.definition.subagent_ids,
            )

            turn_display.start()
            try:
                result = await agent.run(task)
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
                    message_id=new_ulid(),
                    run_id=run_id,
                    parent_execution_id=execution_id,
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
