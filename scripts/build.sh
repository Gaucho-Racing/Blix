#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

if [ "$#" -eq 0 ]; then
  set -- ingestion discord
fi
for service in "$@"; do
  case "$service" in
    ingestion|discord|sandbox) ;;
    *) printf 'No production Dockerfile for service: %s\n' "$service" >&2; exit 1 ;;
  esac
  docker build -t "blix-$service:local" -f "$repo_dir/$service/Dockerfile" "$repo_dir/$service"
done
