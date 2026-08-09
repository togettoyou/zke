FROM registry.k8s.io/kubectl:v1.35.0 AS kubectl

FROM alpine:3.22

COPY --from=kubectl /bin/kubectl /usr/local/bin/kubectl

RUN apk add --no-cache bash bash-completion ca-certificates curl jq \
    && kubectl completion bash > /etc/bash/kubectl-completion.sh \
    && adduser -D -u 1000 -h /workspace zke

USER 1000:1000
WORKDIR /workspace

CMD ["/bin/sh"]
