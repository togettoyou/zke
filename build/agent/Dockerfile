# syntax=docker/dockerfile:1.7

# No `--platform` here, unlike the Go stage below: this stage exists only to
# copy a kubectl binary into the runtime image, so it has to be the target
# architecture's.
FROM registry.k8s.io/kubectl:v1.35.0 AS kubectl

# Pinned to the machine doing the building rather than to the image's target
# architecture: Go cross-compiles from one host with CGO disabled, whereas the
# default (target) platform puts the whole linux/arm64 compile through QEMU.
# Pinning also makes the stage identical across target platforms, so a
# two-architecture build compiles once instead of twice.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS agent-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
# Declared next to the only command that reads it. The version changes on every
# commit, and a build argument participates in the cache key of the commands
# that use it — keeping it below the module download leaves that layer alone.
#
# The build context excludes `.git`, so the caller supplies the version:
#   docker build --build-arg VERSION="$(git describe --tags --always --dirty)" ...
# An unstamped image still runs; it just reports "development".
ARG VERSION=development
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/togettoyou/zke/pkg/shared/buildinfo.version=${VERSION}" \
    -o /out/zke-agent ./cmd/zke-agent

FROM alpine:3.22 AS agent
RUN apk add --no-cache bash bash-completion ca-certificates curl jq tzdata \
    && install -d /etc/zke /workspace /workspace/.kube
COPY --from=kubectl /bin/kubectl /usr/local/bin/kubectl
COPY --from=agent-build /out/zke-agent /usr/local/bin/zke-agent
COPY configs/zke-agent.yaml /etc/zke/zke-agent.yaml
RUN kubectl completion bash > /etc/bash/kubectl-completion.sh
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/zke-agent"]
CMD ["--config", "/etc/zke/zke-agent.yaml"]
