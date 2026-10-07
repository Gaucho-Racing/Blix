import json
import shlex
import subprocess
import sys
from pathlib import Path, PurePosixPath

POLICY_FILE = Path("/results/documentation-policy.json")
DOCUMENTATION_SUFFIXES = {".md", ".mdx", ".rst", ".adoc", ".asciidoc", ".tex"}
DOCUMENTATION_DIRECTORIES = {"doc", "docs", "documentation"}
DOCUMENTATION_NAMES = {"readme", "changelog", "contributing", "authors", "code_of_conduct"}


def validate_files(files: list[str], allowed: bool) -> None:
    if allowed:
        return
    blocked = []
    for filename in files:
        path = PurePosixPath(filename.lower())
        if (
            path.suffix in DOCUMENTATION_SUFFIXES
            or path.name.split(".", 1)[0] in DOCUMENTATION_NAMES
            or any(part in DOCUMENTATION_DIRECTORIES for part in path.parts[:-1])
        ):
            blocked.append(filename)
    if blocked:
        raise RuntimeError(
            "Documentation changes were not explicitly authorized: "
            + ", ".join(blocked)
            + ". Remove these changes before committing or publishing. Do not bypass the policy hooks."
        )


def check_policy(mode: str = "head", policy_file: Path = POLICY_FILE) -> None:
    policy = json.loads(policy_file.read_text())
    if policy["allowed"]:
        return
    targets = ["HEAD"]
    if mode == "push":
        targets = [fields[1] for line in sys.stdin if len(fields := line.split()) == 4 and set(fields[1]) != {"0"}]
    for target in targets:
        arguments = ["git", "diff", "--name-only", "--no-renames", "-z"]
        arguments += ["--cached", policy["base"]] if mode == "commit" else [policy["base"], target]
        result = subprocess.run(
            [*arguments, "--"], cwd=policy["repository"], capture_output=True, text=True, check=True, timeout=30
        )
        validate_files(result.stdout.rstrip("\0").split("\0") if result.stdout else [], False)


def configure_policy(repository: Path, results: Path, base: str, allowed: bool) -> None:
    policy_file = results / POLICY_FILE.name
    policy_file.write_text(json.dumps({"repository": str(repository), "base": base, "allowed": allowed}))
    hooks = results / "git-policy-hooks"
    hooks.mkdir(exist_ok=True)
    for name, mode in [("pre-commit", "commit"), ("pre-push", "push")]:
        hook = hooks / name
        hook.write_text(
            "#!/bin/sh\nexec "
            + shlex.join([sys.executable, str(Path(__file__).resolve()), mode, str(policy_file)])
            + "\n"
        )
        hook.chmod(0o755)
    subprocess.run(["git", "config", "core.hooksPath", str(hooks)], cwd=repository, check=True, timeout=30)


if __name__ == "__main__":
    try:
        check_policy(sys.argv[1], Path(sys.argv[2]))
    except Exception as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1) from error
