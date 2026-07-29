#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

cd "${repository_root}"
AGENT_PROTOCOL_OUTPUT_DIRECTORY="${temporary_directory}" \
  ./hack/generate-agent-protocol.sh >/dev/null

generated_directory="${temporary_directory}/api/agent/v1"
checked_directory="${repository_root}/api/agent/v1"

for generated in "${generated_directory}"/*.pb.go; do
  name="$(basename "${generated}")"
  if [[ ! -f "${checked_directory}/${name}" ]]; then
    echo "generated Agent protocol source is missing: api/agent/v1/${name}" >&2
    exit 1
  fi
  if ! diff -u "${checked_directory}/${name}" "${generated}"; then
    echo "generated Agent protocol source is stale: api/agent/v1/${name}" >&2
    exit 1
  fi
done

for checked in "${checked_directory}"/*.pb.go; do
  name="$(basename "${checked}")"
  if [[ ! -f "${generated_directory}/${name}" ]]; then
    echo "obsolete generated Agent protocol source: api/agent/v1/${name}" >&2
    exit 1
  fi
done

echo "Agent protocol generated sources are current"
