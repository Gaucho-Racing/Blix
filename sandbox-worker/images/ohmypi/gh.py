import os
import subprocess
import sys

from documentation_policy import check_policy
from github_credentials import credentials


def main() -> int:
    if sys.argv[1:3] == ["pr", "create"]:
        check_policy()
    environment = {**os.environ, "GH_TOKEN": credentials()["token"], "GH_HOST": "github.com", "GH_PROMPT_DISABLED": "1"}
    return subprocess.run(["/usr/bin/gh", *sys.argv[1:]], env=environment).returncode


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
