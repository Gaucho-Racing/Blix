#!/usr/bin/env bash
set -euo pipefail

service_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
repository_dir="$(cd -- "${service_dir}/.." && pwd)"

exec uv run --project "${service_dir}" --env-file "${repository_dir}/.env" -- blix-agent "$@"
