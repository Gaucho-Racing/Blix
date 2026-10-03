#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)

if [ "$#" -eq 0 ]; then
  set -- runway knowledge ingestion discord cli
fi
for service in "$@"; do
  case "$service" in
    runway|knowledge|ingestion|discord|cli) ;;
    *) printf 'Unknown Go service: %s\n' "$service" >&2; exit 1 ;;
  esac
  (
    cd "$repo_dir/$service"
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
      printf '%s\n' "$unformatted" >&2
      exit 1
    fi
    go vet ./...
    go test -race ./...
    go build ./...
  )
done
