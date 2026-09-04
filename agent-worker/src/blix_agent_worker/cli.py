import asyncio
import os
from datetime import UTC, datetime

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
        print(f"Blix CLI ({agent.definition.id}, {MODEL})")
        print("Commands: /help, /reset, /tools, /quit")

        while True:
            try:
                user_input = input("you> ").strip()
            except EOFError:
                print()
                return

            if not user_input:
                continue
            if user_input == "/quit":
                return
            if user_input == "/help":
                print("Enter a message, or use /reset, /tools, or /quit.")
                continue
            if user_input == "/reset":
                conversation_id = new_ulid()
                messages.clear()
                print("Conversation reset.")
                continue
            if user_input == "/tools":
                for definition in registry.definitions(agent.definition.tool_ids):
                    print(f"{definition.id}: {definition.description}")
                continue
            if user_input.startswith("/"):
                print(f"error> Unknown command: {user_input}")
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
                print("error> The agent timed out.")
                continue

            if result.status is not AgentResultStatus.SUCCEEDED:
                message = result.error.message if result.error is not None else "The agent failed."
                print(f"error> {message}")
                continue

            try:
                output = GeneralConversationV1Output.model_validate(result.output)
            except ValidationError:
                print("error> The agent returned an invalid conversation response.")
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
            print(f"blix> {output.message}")


def main() -> None:
    try:
        asyncio.run(run_cli())
    except KeyboardInterrupt:
        print()
    except RuntimeError as error:
        raise SystemExit(f"error: {error}") from error
