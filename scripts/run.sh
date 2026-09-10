#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cli_dir="$script_dir/../cli"
cd "$cli_dir"
go build -o cli .
exec ./cli
