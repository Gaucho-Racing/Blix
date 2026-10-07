import argparse
import json
import re
import subprocess
from pathlib import Path

from documentation_policy import configure_policy

REPOSITORY = Path("/workspace/repo")
RESULTS = Path("/results")
TASK_FILE = Path("/workspace/task.json")


def git(*arguments: str, directory: Path | None = None) -> str:
    return subprocess.run(
        ["git", *arguments], cwd=directory or REPOSITORY, capture_output=True, text=True, check=True, timeout=180
    ).stdout.strip()


def prepare(repository: str, base_branch: str | None = None, *, documentation: bool = False) -> dict:
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Use a GitHub owner/repository name.")
    access = json.loads(TASK_FILE.read_text())["repository_access"]
    owner = repository.split("/", 1)[0].lower()
    if repository.lower() not in access and f"{owner}/*" not in access:
        raise ValueError("Repository is not in SANDBOX_REPOSITORIES.")
    if base_branch:
        git("check-ref-format", "--branch", base_branch, directory=REPOSITORY.parent)
    checkout = RESULTS / "checkout.json"
    if checkout.exists():
        metadata = json.loads(checkout.read_text())
        if metadata["repository"].lower() != repository.lower() or (
            base_branch and metadata["base_branch"] != base_branch
        ):
            raise ValueError("This workspace already has a different checkout; ask Blix for a separate task.")
    else:
        arguments = ["clone"]
        if base_branch:
            arguments += ["--branch", base_branch]
        git(*arguments, "--", f"https://github.com/{repository}.git", str(REPOSITORY), directory=REPOSITORY.parent)
        metadata = {
            "repository": repository,
            "base_branch": git("symbolic-ref", "--short", "HEAD"),
            "base_sha": git("rev-parse", "HEAD"),
        }
        checkout.write_text(json.dumps(metadata))
    configure_policy(REPOSITORY, RESULTS, metadata["base_sha"], documentation)
    return metadata


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("repository")
    parser.add_argument("--base")
    parser.add_argument("--allow-documentation-changes", action="store_true")
    arguments = parser.parse_args()
    try:
        print(
            json.dumps(
                prepare(arguments.repository, arguments.base, documentation=arguments.allow_documentation_changes)
            )
        )
    except Exception as error:
        parser.exit(1, f"Checkout failed: {error}\n")
