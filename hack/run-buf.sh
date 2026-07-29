#!/usr/bin/env bash

set -euo pipefail

readonly buf_version="v1.72.0"

exec go run "github.com/bufbuild/buf/cmd/buf@${buf_version}" "$@"
