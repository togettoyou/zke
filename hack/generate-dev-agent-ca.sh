#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_directory="${repository_root}/.local/development"
certificate_file="${output_directory}/agent-ca.crt"
private_key_file="${output_directory}/agent-ca.key"

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required" >&2
  exit 1
fi

mkdir -p "${output_directory}"
if [[ -e "${certificate_file}" || -e "${private_key_file}" ]]; then
  if [[ -f "${certificate_file}" && -f "${private_key_file}" ]]; then
    echo "development Agent CA already exists in ${output_directory}"
    exit 0
  fi
  echo "development Agent CA is incomplete; remove both files before regenerating" >&2
  exit 1
fi

umask 077
temporary_directory="$(mktemp -d "${output_directory}/agent-ca.XXXXXX")"
trap 'rm -rf "${temporary_directory}"' EXIT

openssl ecparam \
  -name prime256v1 \
  -genkey \
  -noout \
  -out "${temporary_directory}/agent-ca.key"

openssl req \
  -x509 \
  -new \
  -sha256 \
  -key "${temporary_directory}/agent-ca.key" \
  -days 3650 \
  -subj "/O=ZKE/CN=ZKE Development Agent CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out "${temporary_directory}/agent-ca.crt"

mv "${temporary_directory}/agent-ca.key" "${private_key_file}"
mv "${temporary_directory}/agent-ca.crt" "${certificate_file}"
chmod 600 "${private_key_file}"
chmod 644 "${certificate_file}"

echo "generated development Agent CA in ${output_directory}"
