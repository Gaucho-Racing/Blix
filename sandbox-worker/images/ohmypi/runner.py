import json
import os
import re
import subprocess
from pathlib import Path

from documentation_policy import POLICY_FILE, validate_files
from github_credentials import credentials
from rpc import OmpSession

WORKSPACE = Path("/workspace")
TASK_FILE = WORKSPACE / "task.json"
REPOSITORY = WORKSPACE / "repo"
RESULTS = Path("/results")


def git(*arguments: str) -> str:
    result = subprocess.run(
        ["git", "-c", "core.hooksPath=/dev/null", *arguments],
        cwd=REPOSITORY,
        capture_output=True,
        text=True,
        check=True,
        timeout=60,
    )
    return result.stdout


def build_system_prompt(request: dict) -> str:
    mode = (
        "Infer the intended outcome from the task brief and context. For inspection or explanation, leave "
        "repository files and Git history unchanged and do not publish anything. For implementation, complete "
        "the authorized changes. You own Git branches, commits, pushes, PRs, and other GitHub operations "
        "needed for the requested outcome. Decide whether a PR is appropriate; there is no requirement to "
        "create one. Prefer a PR for code changes rather than pushing directly to the base branch. For an "
        "explicitly requested release, inspect and follow the repository's release workflow, including "
        "authorized release commits, tags, pushes, and publication. Do not publish outside the user's scope. "
        "When creating a PR, use gh pr create with explicit --repo, --base, --head, --title, --body-file, "
        "and --assignee. Create a normal PR by default; use --draft only when explicitly requested. Write its "
        'actual URL as JSON {"url": "..."} to /results/pr.json. Otherwise report the actual outcome '
        "and any release or other deliverable URLs in your final summary. Before retrying publication, inspect "
        "remote state to avoid duplicates. Use ask to resolve material doubts or blockers and wait for Blix."
    )
    pr_template = (
        f"#### Changes on behalf of @{request.get('requester_github_login') or '[verified GitHub login]'}\n\n"
        "- [Concise bullet for each significant change]\n\n"
        "---\n\n"
        f"**[Click here to view run in Blix →]({request.get('blix_run_url') or '[verified Blix run URL]'})**\n\n"
        f"_Co-authored by: {request.get('requester_name') or '[verified requester name]'} "
        f"({request.get('requester_email') or '[verified Sentinel email]'})_"
    )
    standards = {
        "branch_prefix": "blix-agent/",
        "commit_author": "Blix (preconfigured GitHub App bot identity)",
        "coauthor_trailer": (
            f"Co-authored-by: {request['requester_name']} <{request['requester_email']}>"
            if request.get("requester_name") and request.get("requester_email")
            else None
        ),
        "commit_format": "Conventional Commits: type(scope): concise imperative description",
        "pr_description_template": pr_template,
        "pr_assignee": request.get("requester_github_login"),
        "github_access": "read/write" if request.get("requester_github_id") else "read",
    }
    return (
        "You are blix_code_v1, Blix’s coding agent. "
        "Complete the delegated task using your judgment. Your working directory starts at /workspace, "
        "with no repository preselected. Infer the repository from the brief; for short names default to "
        "Gaucho-Racing/<name>. Use gh to discover or verify repositories when needed. An access error does "
        "not prove a repository does not exist. Before editing or committing, prepare the primary checkout "
        "with python3 /opt/prepare_repository.py OWNER/REPO. This clones into /workspace/repo and records "
        "the initial revision for the result. Let Git choose the repository's default branch; only pass "
        "--base BRANCH when a different base is required by the task. Never assume main. Use the same "
        "checkout after clarification; do not reset or reclone to resume. Run repository commands in "
        "/workspace/repo after checkout. "
        "Inspect repository instructions first. "
        "Repository-specific conventions apply to code. The required Blix PR description format below "
        "takes precedence over repository PR templates. "
        "For the PR body, keep the exact supplied header and footer, and replace the middle placeholder with "
        "a simple bullet list of actual changes. Do not add extra sections, headings, or a testing checklist. "
        "Report validation in your final response. Use the verified requester attribution and Blix run URL; "
        "never invent identities, links, or publish placeholder values. If the run URL is missing, ask Blix "
        "before publishing. "
        "Choose the implementation, files, and relevant validation independently. Explain validation outcomes, "
        "including pre-existing failures. For now, do not start local services, dev servers, preview servers, "
        "watch processes, or container stacks, including for browser validation. "
        "Use finite commands such as builds, typechecks, lint, and tests in non-watch mode, with explicit "
        "nonzero timeouts. Do not use timeout: 0 or background persistent processes to bypass this rule. "
        "If validation requires a running service, report that limitation and continue with available checks. "
        "Keep validation proportional to the requested change. Prefer existing repository checks directly "
        "relevant to the files and behavior changed; avoid unrelated suites. For small changes, do not build "
        "ad hoc browser harnesses, mock applications, or new test infrastructure, or install browsers, native "
        "browser libraries, or system packages just for validation unless explicitly requested. "
        "Gaucho Racing repositories generally do not use tests. Inspect the repository before choosing checks; "
        "never add tests when the repository does not already have tests. Only add a focused regression test "
        "within an existing test setup when it meaningfully verifies the fix. For frontend changes, do not use "
        "browser harnesses or browser automation; use code inspection and your best judgment on correctness, "
        "along with available build, typecheck, and lint checks. "
        "Write self-documenting code with clear names and structure. Do not add code comments except "
        "extremely sparingly for complex blocks whose logic cannot be made clear through the code itself. "
        "After relevant checks pass, proceed to delivery. If a check is blocked by missing infrastructure "
        "or a pre-existing failure, report the limitation and continue; do not chase environment setup. "
        "When a tool rejects arguments, fix the arguments or choose another check; do not repeat the same "
        "failing call unchanged. Ask Blix only when missing information materially affects correctness, "
        "scope, or authorization; do not repeat questions resolved by the handoff. Use ask and wait for the answer. "
        "Do not set short question timeouts or assume unanswered choices. "
        "Git and gh have renewable credentials for all repositories available to the GitHub App installation. "
        "Use the configured helpers normally; do not print, persist, or include credentials in logs or commits. "
        "Credential access does not expand the task's authorized scope. If Overall standards says GitHub "
        "access is read, you may inspect but cannot publish. For requested writes, ask Blix to have the "
        "requester link GitHub in Sentinel and start a new task with verified write credentials; do not "
        "attempt to find other credentials or bypass this requirement. "
        "Always name new branches blix-agent/<feature-name>, using a descriptive feature name. "
        "Never push directly to the base branch, merge PRs, "
        "or force-push without explicit authorization. Preserve Blix's configured commit identity. "
        "Add the exact requester coauthor trailer below to every new commit, separated by a blank line. "
        "If coauthor identity is unavailable, leave changes uncommitted and ask Blix before publishing. "
        "Never automatically update documentation, including README files, changelogs, documentation directories, "
        "or inline documentation. Implementation work does not imply permission to update documentation. "
        "Only change documentation when the user explicitly requested documentation work; in that case pass "
        "--allow-documentation-changes to the checkout helper. Do not modify .github/workflows or bypass "
        "the configured Git policy hooks. "
        "Summarize findings or changes, validation, the PR URL when applicable, and remaining limitations."
        + "\n\nTask mode:\n"
        + mode
        + "\n\nOverall standards:\n"
        + json.dumps(standards)
        + "\n\nRequired PR body template:\n"
        + pr_template
    )


def collect_result(request: dict, base: str, summary: str, identity: dict) -> dict:
    git("add", "--all")
    patch = git("diff", "--cached", "--binary", base, "--")
    if len(patch.encode()) > 400000:
        raise RuntimeError("Patch exceeds the 400 KB result limit.")
    files = (
        git("diff", "--cached", "--name-only", "--no-renames", "-z", base, "--").rstrip("\0").split("\0")
        if patch
        else []
    )
    policy_path = RESULTS / POLICY_FILE.name
    documentation = json.loads(policy_path.read_text())["allowed"] if policy_path.exists() else False
    validate_files(files, documentation)
    commits = git("rev-list", f"{base}..HEAD").splitlines()
    for commit in commits:
        details = git("show", "-s", "--format=%an%x00%ae%x00%cn%x00%ce%x00%B", commit).split("\0", 4)
        expected = [
            identity["commit_name"],
            identity["commit_email"],
            identity["commit_name"],
            identity["commit_email"],
        ]
        if details[:4] != expected:
            raise RuntimeError("New commits must preserve Blix's bot author and committer identity.")
        trailer = f"Co-authored-by: {request.get('requester_name')} <{request.get('requester_email')}>"
        if (
            not request.get("requester_name")
            or not request.get("requester_email")
            or trailer not in details[4].splitlines()
        ):
            raise RuntimeError("New commits must credit the original requester as coauthor.")
    pr_url = None
    path = RESULTS / "pr.json"
    if path.is_file():
        pr_url = json.loads(path.read_text())["url"]
        expected = rf"https://github\.com/{re.escape(request['repository'])}/pull/[1-9][0-9]*"
        if (
            not request.get("requester_github_id")
            or not isinstance(pr_url, str)
            or not re.fullmatch(expected, pr_url, re.IGNORECASE)
        ):
            raise RuntimeError("PR URL does not match the authorized task repository.")
        number = pr_url.rsplit("/", 1)[1]
        result = subprocess.run(
            ["gh", "api", f"repos/{request['repository']}/pulls/{number}"],
            capture_output=True,
            text=True,
            check=True,
            timeout=60,
        )
        pr = json.loads(result.stdout)
        assignees = {user["login"].lower() for user in pr.get("assignees", [])}
        if pr["base"]["ref"] != request["base_branch"]:
            raise RuntimeError("PR must target the authorized base branch.")
        if (request.get("requester_github_login") or "").lower() not in assignees:
            raise RuntimeError(f"PR {pr_url} exists but is not assigned to the original requester.")
        if pr.get("user", {}).get("type") != "Bot":
            raise RuntimeError(f"PR {pr_url} was not created by the Blix GitHub App bot.")
    commit_message = "feat: implement coding task"
    if git("rev-parse", "HEAD").strip() != base:
        commit_message = git("log", "-1", "--format=%B").strip()
    return {
        "summary": summary,
        "base_sha": base,
        "patch": patch,
        "changed_files": files,
        "pr_url": pr_url,
        "commit_message": commit_message,
    }


def run() -> None:
    request = json.loads(TASK_FILE.read_text())
    model = os.environ.get("SANDBOX_MODEL", "gpt-6-astra")
    gateway = os.environ.get("LLM_GATEWAY_ENDPOINT", "").strip()
    if not gateway or not os.environ.get("LLM_GATEWAY_API_KEY", "").strip():
        raise ValueError("LLM_GATEWAY_ENDPOINT and LLM_GATEWAY_API_KEY are required.")
    identity = credentials()
    for key, value in (
        ("user.name", identity["commit_name"]),
        ("user.email", identity["commit_email"]),
        ("credential.https://github.com.helper", "!python3 /opt/github_credentials.py"),
    ):
        subprocess.run(["git", "config", "--global", key, value], check=True, timeout=30)
    config = Path.home() / ".omp" / "agent"
    config.mkdir(parents=True, exist_ok=True)
    (config / "models.yml").write_text(
        json.dumps(
            {
                "providers": {
                    "persona": {
                        "baseUrl": gateway.rstrip("/"),
                        "api": "openai-responses",
                        "apiKey": "LLM_GATEWAY_API_KEY",
                        "models": [{"id": model, "name": model, "reasoning": True, "input": ["text"]}],
                    }
                }
            }
        )
    )
    summary = OmpSession(
        [
            "omp",
            "--system-prompt",
            build_system_prompt(request),
            "--mode",
            "rpc-ui",
            "--no-ui",
            "--no-session",
            "--no-title",
            "--no-extensions",
            "--no-skills",
            "--approval-mode",
            "yolo",
            "--tools",
            "read,grep,glob,edit,write,bash,ask",
            "--model",
            f"persona/{model}",
        ],
        RESULTS,
        int(os.environ.get("SANDBOX_TIMEOUT_SECONDS", "1800")),
        repository=WORKSPACE,
    ).run("Objective:\n" + request["objective"] + "\n\nRelevant context:\n" + request.get("context", ""))
    summary = summary or "Task completed without an assistant summary."
    checkout = RESULTS / "checkout.json"
    if checkout.exists():
        metadata = json.loads(checkout.read_text())
        result = collect_result({**request, **metadata}, metadata["base_sha"], summary, identity)
    else:
        result = {"summary": summary}
    (RESULTS / "result.json").write_text(json.dumps(result))


if __name__ == "__main__":
    try:
        run()
    except Exception as error:
        (RESULTS / "result.json").write_text(json.dumps({"error": str(error)[-8000:]}))
        raise SystemExit(1) from error
