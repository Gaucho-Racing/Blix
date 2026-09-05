#!/usr/bin/env sh
set -eu

docker compose up --build -d runway agent-worker
docker compose run --build --rm --no-deps cli
