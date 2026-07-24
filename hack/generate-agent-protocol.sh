#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v protoc >/dev/null 2>&1; then
  echo "protoc is required" >&2
  exit 1
fi

temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

cd "${repository_root}"
GOBIN="${temporary_directory}" go install google.golang.org/protobuf/cmd/protoc-gen-go
PATH="${temporary_directory}:${PATH}" protoc \
  --go_out=. \
  --go_opt=module=github.com/togettoyou/zke \
  api/agent/v1/control.proto

echo "generated Agent protocol Go sources"
