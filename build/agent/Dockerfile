# syntax=docker/dockerfile:1.7

FROM registry.k8s.io/kubectl:v1.35.0 AS kubectl

FROM golang:1.26.4-alpine AS agent-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/zke-agent ./cmd/zke-agent

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
