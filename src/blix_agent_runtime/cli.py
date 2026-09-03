import argparse
import asyncio
import os
import sys

from blix_agent_runtime.errors import AgentRuntimeError, ConfigurationError
from blix_agent_runtime.gateway import LiteLLMGateway
from blix_agent_runtime.runtime import AgentRuntime


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="blix-agent", description="Run the Blix agent runtime locally")
    parser.add_argument("--model", default=os.getenv("BLIX_MODEL"))
    parser.add_argument("--api-base", default=os.getenv("BLIX_API_BASE"))
    parser.add_argument(
        "--timeout-seconds",
        type=float,
        default=float(os.getenv("BLIX_TIMEOUT_SECONDS", "120")),
    )
    parser.add_argument(
        "--max-tool-rounds",
        type=int,
        default=int(os.getenv("BLIX_MAX_TOOL_ROUNDS", "8")),
    )
    return parser


async def run_cli(args: argparse.Namespace) -> None:
    if not args.model:
        raise ConfigurationError("Set BLIX_MODEL or pass --model, for example --model openai/<model-name>")

    runtime = AgentRuntime(
        gateway=LiteLLMGateway(
            model=args.model,
            api_base=args.api_base,
            timeout_seconds=args.timeout_seconds,
        ),
        max_tool_rounds=args.max_tool_rounds,
    )

    print(f"Blix agent runtime ({args.model})")
    print("Commands: /agents, /help, /reset, /quit")

    while True:
        try:
            user_input = await asyncio.to_thread(input, "you> ")
        except EOFError:
            print()
            return
        except KeyboardInterrupt:
            print()
            return

        command = user_input.strip().lower()
        if command in {"/quit", "/exit"}:
            return
        if command == "/reset":
            runtime.reset()
            print("blix> Conversation reset.")
            continue
        if command == "/agents":
            for name, description in runtime.agent_summaries():
                print(f"{name}: {description}")
            continue
        if command == "/help":
            print("/agents  List configured agents")
            print("/reset   Clear conversation history")
            print("/quit    Exit the runtime")
            continue
        if not command:
            continue

        try:
            response = await runtime.chat(user_input)
        except AgentRuntimeError as error:
            print(f"error> {error}", file=sys.stderr)
            continue
        print(f"blix> {response}")


def main() -> None:
    args = build_parser().parse_args()
    try:
        asyncio.run(run_cli(args))
    except ConfigurationError as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2) from error
