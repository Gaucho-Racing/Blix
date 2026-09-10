#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

choose_callback_port() {
  attempt=0
  while [ "$attempt" -lt 50 ]; do
    random_value=$(od -An -N2 -tu2 /dev/urandom | tr -d ' ')
    candidate=$((49152 + random_value % 16384))
    if ! nc -z 127.0.0.1 "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
    attempt=$((attempt + 1))
  done
  return 1
}

callback_port=${BLIX_CLI_CALLBACK_PORT:-}
if [ -z "$callback_port" ]; then
  callback_port=$(choose_callback_port) || {
    echo "Could not find an available OAuth callback port." >&2
    exit 1
  }
fi

case "$callback_port" in
  *[!0-9]* | "")
    echo "BLIX_CLI_CALLBACK_PORT must be a numeric TCP port." >&2
    exit 1
    ;;
esac
if [ "$callback_port" -lt 1024 ] || [ "$callback_port" -gt 65535 ]; then
  echo "BLIX_CLI_CALLBACK_PORT must be between 1024 and 65535." >&2
  exit 1
fi

credentials_dir=${BLIX_CREDENTIALS_DIR:-"$HOME/.blix"}
mkdir -p "$credentials_dir"

export BLIX_CLI_CALLBACK_PORT="$callback_port"
export BLIX_CLI_UID="$(id -u)"
export BLIX_CLI_GID="$(id -g)"
export BLIX_CREDENTIALS_DIR="$credentials_dir"

cd "$repo_dir"
exec docker compose --profile cli run --rm --build \
  --publish "127.0.0.1:${callback_port}:${callback_port}" \
  cli
