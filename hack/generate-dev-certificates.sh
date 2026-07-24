#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_directory="${repository_root}/.local/development"
agent_ca_certificate_file="${output_directory}/agent-ca.crt"
agent_ca_private_key_file="${output_directory}/agent-ca.key"
server_ca_certificate_file="${output_directory}/server-ca.crt"
server_ca_private_key_file="${output_directory}/server-ca.key"
server_certificate_file="${output_directory}/zke-server.crt"
server_private_key_file="${output_directory}/zke-server.key"

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required" >&2
  exit 1
fi

mkdir -p "${output_directory}"
validate_pair() {
  local certificate_path="$1"
  local private_key_path="$2"
  local description="$3"
  if [[ -e "${certificate_path}" || -e "${private_key_path}" ]] &&
    [[ ! -f "${certificate_path}" || ! -f "${private_key_path}" ]]; then
    echo "${description} is incomplete; remove both files before regenerating" >&2
    exit 1
  fi
}

validate_pair \
  "${agent_ca_certificate_file}" \
  "${agent_ca_private_key_file}" \
  "development Agent CA"
validate_pair \
  "${server_ca_certificate_file}" \
  "${server_ca_private_key_file}" \
  "development Server CA"
validate_pair \
  "${server_certificate_file}" \
  "${server_private_key_file}" \
  "development ZKE Server identity"

if [[ -f "${agent_ca_certificate_file}" &&
  -f "${agent_ca_private_key_file}" &&
  -f "${server_ca_certificate_file}" &&
  -f "${server_ca_private_key_file}" &&
  -f "${server_certificate_file}" &&
  -f "${server_private_key_file}" ]]; then
  echo "development certificates already exist in ${output_directory}"
  exit 0
fi

umask 077
temporary_directory="$(mktemp -d "${output_directory}/certificates.XXXXXX")"
trap 'rm -rf "${temporary_directory}"' EXIT

if [[ ! -f "${agent_ca_certificate_file}" ]]; then
  openssl ecparam \
    -name prime256v1 \
    -genkey \
    -noout \
    -out "${temporary_directory}/agent-ca.key"

  # Git Bash/MSYS otherwise rewrites slash-prefixed subjects as Windows paths.
  MSYS2_ARG_CONV_EXCL="/O=" openssl req \
    -x509 \
    -new \
    -sha256 \
    -key "${temporary_directory}/agent-ca.key" \
    -days 3650 \
    -subj "/O=ZKE/CN=ZKE Development Agent CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "${temporary_directory}/agent-ca.crt"

  mv "${temporary_directory}/agent-ca.key" "${agent_ca_private_key_file}"
  mv "${temporary_directory}/agent-ca.crt" "${agent_ca_certificate_file}"
fi

if [[ ! -f "${server_ca_certificate_file}" ]]; then
  openssl ecparam \
    -name prime256v1 \
    -genkey \
    -noout \
    -out "${temporary_directory}/server-ca.key"

  MSYS2_ARG_CONV_EXCL="/O=" openssl req \
    -x509 \
    -new \
    -sha256 \
    -key "${temporary_directory}/server-ca.key" \
    -days 3650 \
    -subj "/O=ZKE/CN=ZKE Development Server CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "${temporary_directory}/server-ca.crt"

  mv "${temporary_directory}/server-ca.key" "${server_ca_private_key_file}"
  mv "${temporary_directory}/server-ca.crt" "${server_ca_certificate_file}"
fi

if [[ ! -f "${server_certificate_file}" ]]; then
  openssl ecparam \
    -name prime256v1 \
    -genkey \
    -noout \
    -out "${temporary_directory}/zke-server.key"

  MSYS2_ARG_CONV_EXCL="/O=" openssl req \
    -new \
    -sha256 \
    -key "${temporary_directory}/zke-server.key" \
    -subj "/O=ZKE/CN=localhost" \
    -out "${temporary_directory}/zke-server.csr"

  cat >"${temporary_directory}/zke-server-extensions.cnf" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1
EOF

  openssl x509 \
    -req \
    -sha256 \
    -in "${temporary_directory}/zke-server.csr" \
    -CA "${server_ca_certificate_file}" \
    -CAkey "${server_ca_private_key_file}" \
    -CAserial "${temporary_directory}/server-ca.srl" \
    -CAcreateserial \
    -days 825 \
    -extfile "${temporary_directory}/zke-server-extensions.cnf" \
    -out "${temporary_directory}/zke-server.crt"

  mv "${temporary_directory}/zke-server.key" "${server_private_key_file}"
  mv "${temporary_directory}/zke-server.crt" "${server_certificate_file}"
fi

chmod 600 \
  "${agent_ca_private_key_file}" \
  "${server_ca_private_key_file}" \
  "${server_private_key_file}"
chmod 644 \
  "${agent_ca_certificate_file}" \
  "${server_ca_certificate_file}" \
  "${server_certificate_file}"

echo "generated development certificates in ${output_directory}"
