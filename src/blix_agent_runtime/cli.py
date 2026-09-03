import argparse
import asyncio
import os
import sys

from blix_agent_runtime.errors import AgentRuntimeError, ConfigurationError
from blix_agent_runtime.gateway import MODEL_NAME, BifrostGateway
from blix_agent_runtime.runtime import AgentRuntime


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="blix-agent", description="Run the Blix agent runtime locally")
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
    username = os.getenv("BLIX_BIFROST_USERNAME")
    password = os.getenv("BLIX_BIFROST_PASSWORD")
    if not username or not password:
        raise ConfigurationError("Set BLIX_BIFROST_USERNAME and BLIX_BIFROST_PASSWORD")

    gateway = BifrostGateway(
        username=username,
        password=password,
        timeout_seconds=args.timeout_seconds,
    )
    runtime = AgentRuntime(
        gateway=gateway,
        max_tool_rounds=args.max_tool_rounds,
    )

    print(f"Blix agent runtime ({MODEL_NAME})")
    print("Commands: /agents, /help, /reset, /quit")

    try:
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
    finally:
        await gateway.close()


def main() -> None:
    args = build_parser().parse_args()
    try:
        asyncio.run(run_cli(args))
    except ConfigurationError as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2) from error
