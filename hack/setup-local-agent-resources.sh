#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
namespace="zke-system"
listener_ca_file="${repository_root}/data/pki/agent-listener-ca.crt"
kube_context=""

usage() {
  cat <<'EOF'
Usage: hack/setup-local-agent-resources.sh [options]

Create or update the Kubernetes resources required by a host-run ZKE Agent.
The Enrollment Token is read without echo from the terminal, or from one line
on standard input when the script is used non-interactively.

Options:
  --context NAME             kubectl context (default: current context)
  --namespace NAME           Agent namespace (default: zke-system)
  --listener-ca-file PATH    Agent Listener CA certificate
  -h, --help                 show this help

The script creates or updates:
  Namespace
  Secret/zke-agent-enrollment
  Secret/zke-agent-trust

It never creates, replaces, or deletes Secret/zke-agent-identity.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --context)
      [[ $# -ge 2 ]] || {
        echo "--context requires a value" >&2
        exit 2
      }
      kube_context="$2"
      shift 2
      ;;
    --namespace)
      [[ $# -ge 2 ]] || {
        echo "--namespace requires a value" >&2
        exit 2
      }
      namespace="$2"
      shift 2
      ;;
    --listener-ca-file)
      [[ $# -ge 2 ]] || {
        echo "--listener-ca-file requires a value" >&2
        exit 2
      }
      listener_ca_file="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required" >&2
  exit 1
fi
if [[ ! "${namespace}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  [[ ${#namespace} -gt 63 ]]; then
  echo "namespace must be a valid DNS label" >&2
  exit 2
fi
if [[ ! -f "${listener_ca_file}" ]]; then
  echo "Agent Listener CA certificate not found: ${listener_ca_file}" >&2
  echo "start zke-server once in managed PKI mode before running this script" >&2
  exit 1
fi

kubectl_command=(kubectl)
if [[ -n "${kube_context}" ]]; then
  kubectl_command+=(--context "${kube_context}")
  resolved_context="${kube_context}"
else
  resolved_context="$(kubectl config current-context)"
fi
echo "kubectl context: ${resolved_context}"
echo "Agent namespace: ${namespace}"

if [[ -t 0 ]]; then
  IFS= read -r -s -p "Enrollment Token: " enrollment_token
  echo
else
  IFS= read -r enrollment_token
fi
enrollment_token="${enrollment_token%$'\r'}"
if [[ ! "${enrollment_token}" =~ ^[A-Za-z0-9_-]{43}$ ]]; then
  echo "Enrollment Token must be a 32-byte unpadded Base64URL value" >&2
  exit 2
fi

"${kubectl_command[@]}" create namespace "${namespace}" \
  --dry-run=client \
  -o yaml |
  "${kubectl_command[@]}" apply \
    --server-side \
    --field-manager=zke-local-agent-setup \
    -f -

{
  cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: zke-agent-enrollment
  namespace: ${namespace}
type: Opaque
stringData:
  token: ${enrollment_token}
EOF
} |
  "${kubectl_command[@]}" apply \
    --server-side \
    --field-manager=zke-local-agent-setup \
    -f -

"${kubectl_command[@]}" \
  --namespace "${namespace}" \
  create secret generic zke-agent-trust \
  --from-file="agent-listener-ca.crt=${listener_ca_file}" \
  --dry-run=client \
  -o yaml |
  "${kubectl_command[@]}" apply \
    --server-side \
    --field-manager=zke-local-agent-setup \
    -f -

if "${kubectl_command[@]}" \
  --namespace "${namespace}" \
  get secret zke-agent-identity >/dev/null 2>&1; then
  echo "preserved existing Secret/zke-agent-identity"
fi

echo "local Agent bootstrap resources are ready"
echo "run: go run ./cmd/zke-agent --config configs/zke-agent.yaml"
