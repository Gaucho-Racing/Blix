import json
import os
import sys
import urllib.error
import urllib.request


def credentials() -> dict:
    request = urllib.request.Request(
        os.environ["SANDBOX_CREDENTIAL_URL"],
        headers={"Authorization": "Bearer " + os.environ["SANDBOX_CREDENTIAL_SECRET"]},
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            payload = json.load(response)
    except (urllib.error.URLError, ValueError) as error:
        raise RuntimeError("Blix could not provide GitHub credentials.") from error
    if not isinstance(payload, dict) or not isinstance(payload.get("token"), str) or not payload["token"]:
        raise RuntimeError("Blix returned invalid GitHub credentials.")
    return payload


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] != "get":
        return
    fields = dict(line.rstrip("\n").split("=", 1) for line in sys.stdin if "=" in line)
    if fields.get("protocol") != "https" or fields.get("host") != "github.com":
        return
    print("username=x-access-token")
    print("password=" + credentials()["token"])


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("GitHub credential renewal failed.", file=sys.stderr)
        raise SystemExit(1)
