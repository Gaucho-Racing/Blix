#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

if [ "$#" -eq 0 ]; then
  set -- runway knowledge ingestion discord agent-worker web cli sandbox-worker blix-code-v1
fi
for service in "$@"; do
  case "$service" in
    runway|knowledge|ingestion|discord|agent-worker|web|cli|sandbox-worker|blix-code-v1) ;;
    *) printf 'No production Dockerfile for service: %s\n' "$service" >&2; exit 1 ;;
  esac
  context="$repo_dir/$service"
  image="blix-$service:local"
  if [ "$service" = blix-code-v1 ]; then
    context="$repo_dir/sandbox-worker/images/ohmypi"
    image="blix-code-v1:local"
  fi
  docker build -t "$image" -f "$context/Dockerfile" "$context"
done
