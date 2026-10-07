import base64
import json
import os
import subprocess
import tempfile
import time

import httpx


def github_app_configured() -> bool:
    return all(
        os.environ.get(key, "").strip()
        for key in (
            "GITHUB_APP_ID",
            "GITHUB_APP_INSTALLATION_ID",
            "GITHUB_APP_PRIVATE_KEY",
        )
    )


def github_credentials(*, write: bool = True) -> dict[str, str | None]:
    token = os.environ.get("SANDBOX_GITHUB_TOKEN", "").strip()
    if token and not github_app_configured():
        if not os.environ.get("SANDBOX_COMMIT_EMAIL"):
            raise ValueError("SANDBOX_COMMIT_EMAIL must identify the Blix bot when using a static token.")
        return {
            "token": token,
            "expires_at": None,
            "commit_name": "Blix",
            "commit_email": os.environ["SANDBOX_COMMIT_EMAIL"],
        }
    if not github_app_configured():
        raise ValueError("GitHub App credentials or SANDBOX_GITHUB_TOKEN are required.")

    def encode(data: bytes) -> str:
        return base64.urlsafe_b64encode(data).decode().rstrip("=")

    installation = os.environ["GITHUB_APP_INSTALLATION_ID"]
    if not installation.isdigit():
        raise ValueError("GITHUB_APP_INSTALLATION_ID must be numeric.")
    now = int(time.time())
    header = encode(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
    payload = encode(json.dumps({"iat": now - 60, "exp": now + 540, "iss": os.environ["GITHUB_APP_ID"]}).encode())
    message = f"{header}.{payload}"
    with tempfile.NamedTemporaryFile() as key:
        key.write(os.environ["GITHUB_APP_PRIVATE_KEY"].replace("\\n", "\n").encode())
        key.flush()
        signature = subprocess.run(
            ["openssl", "dgst", "-sha256", "-sign", key.name],
            input=message.encode(),
            capture_output=True,
            timeout=10,
        )
    if signature.returncode:
        raise ValueError("GITHUB_APP_PRIVATE_KEY could not sign an installation token request.")
    app_response = httpx.get(
        "https://api.github.com/app",
        headers={"Authorization": f"Bearer {message}.{encode(signature.stdout)}"},
        timeout=30,
    )
    if app_response.status_code != 200 or not app_response.json().get("slug"):
        raise ValueError("Could not resolve the GitHub App bot identity.")
    slug = app_response.json()["slug"]
    permissions = {"contents": "write" if write else "read"}
    if write:
        permissions["pull_requests"] = "write"
    response = httpx.post(
        f"https://api.github.com/app/installations/{installation}/access_tokens",
        headers={
            "Authorization": f"Bearer {message}.{encode(signature.stdout)}",
            "Accept": "application/vnd.github+json",
        },
        json={"permissions": permissions},
        timeout=30,
    )
    if response.status_code != 201:
        raise ValueError(
            f"GitHub App token request failed (HTTP {response.status_code}); "
            "check installation repository access and Contents/Pull requests permissions."
        )
    credentials = response.json()
    if not credentials.get("token") or not credentials.get("expires_at"):
        raise ValueError("GitHub returned incomplete installation credentials.")
    bot_response = httpx.get(
        f"https://api.github.com/users/{slug}%5Bbot%5D",
        headers={"Authorization": "Bearer " + credentials["token"]},
        timeout=30,
    )
    if bot_response.status_code != 200 or not isinstance(bot_response.json().get("id"), int):
        raise ValueError("Could not resolve the GitHub App bot account.")
    return {
        "token": credentials["token"],
        "expires_at": credentials["expires_at"],
        "commit_name": "Blix",
        "commit_email": f"{bot_response.json()['id']}+{slug}[bot]@users.noreply.github.com",
    }


def github_token(*, write: bool = False) -> str:
    if not os.environ.get("SANDBOX_GITHUB_TOKEN", "").strip() and not github_app_configured():
        return ""
    return str(github_credentials(write=write)["token"])
