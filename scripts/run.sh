#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

credentials_dir=${BLIX_CREDENTIALS_DIR:-"$HOME/.blix"}
mkdir -p "$credentials_dir"

export BLIX_CLI_UID="$(id -u)"
export BLIX_CLI_GID="$(id -g)"
export BLIX_CREDENTIALS_DIR="$credentials_dir"

cd "$repo_dir"
exec docker compose --profile cli run --rm --build cli
