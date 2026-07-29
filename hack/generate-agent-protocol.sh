#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly expected_protoc_version="libprotoc 35.0"
output_directory="${AGENT_PROTOCOL_OUTPUT_DIRECTORY:-.}"

if ! command -v protoc >/dev/null 2>&1; then
  echo "protoc is required" >&2
  exit 1
fi

actual_protoc_version="$(protoc --version)"
if [[ "${actual_protoc_version}" != "${expected_protoc_version}" ]]; then
  echo "protoc ${expected_protoc_version#libprotoc } is required; found ${actual_protoc_version}" >&2
  exit 1
fi

cd "${repository_root}"
protoc_gen_go="$(go tool -n protoc-gen-go)"
mkdir -p "${output_directory}"
protoc \
  --plugin="protoc-gen-go=${protoc_gen_go}" \
  --go_out="${output_directory}" \
  --go_opt=module=github.com/togettoyou/zke \
  api/agent/v1/*.proto

echo "generated Agent protocol Go sources"
