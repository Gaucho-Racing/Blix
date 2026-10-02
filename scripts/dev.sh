#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo_dir"

if [ ! -f .env ]; then
  cp example.env .env
  printf '%s\n' 'Created .env from example.env. Configure the service credentials before starting.'
  exit 1
fi

exec docker compose up --build -d "$@"
