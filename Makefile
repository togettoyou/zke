# ZKE development commands.
#
# These wrap the tools the repository already relies on so that a contributor
# and an automated check run exactly the same thing.

GO ?= go
GOLANGCI_LINT ?= golangci-lint

# Version stamped into zke-server and zke-agent.
#
# `git describe` covers both cases with one command: on a tagged commit it
# prints the tag, otherwise the abbreviated commit (plus how far past the last
# tag it is, when there is one). Override VERSION where no Git history is
# available — the container builds do exactly that, since the image context
# excludes `.git`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo development)
GO_LDFLAGS := -X github.com/togettoyou/zke/pkg/shared/buildinfo.version=$(VERSION)

# Integration tests are skipped unless a throwaway PostgreSQL is pointed at.
# Set it explicitly rather than defaulting to a URL that might be a real
# database.
ZKE_TEST_DATABASE_URL ?=

.PHONY: all
all: fmt vet lint test

.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l cmd pkg api); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required for:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: lint
lint:
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint is not installed; see https://golangci-lint.run"; \
		exit 1; \
	}
	$(GOLANGCI_LINT) run

.PHONY: test
test:
	$(GO) test ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

# Runs the PostgreSQL-backed integration tests too. Point
# ZKE_TEST_DATABASE_URL at a disposable database: the tests apply migrations
# and write to it.
.PHONY: test-integration
test-integration:
	@if [ -z "$(ZKE_TEST_DATABASE_URL)" ]; then \
		echo "set ZKE_TEST_DATABASE_URL to a disposable PostgreSQL database"; \
		exit 1; \
	fi
	ZKE_TEST_DATABASE_URL="$(ZKE_TEST_DATABASE_URL)" $(GO) test ./... -count=1

.PHONY: build
build:
	$(GO) build -ldflags="$(GO_LDFLAGS)" ./...

# Release-shaped binaries, stamped the same way the images are.
.PHONY: build-server
build-server:
	$(GO) build -trimpath -ldflags="-s -w $(GO_LDFLAGS)" -o bin/zke-server ./cmd/zke-server

.PHONY: build-agent
build-agent:
	$(GO) build -trimpath -ldflags="-s -w $(GO_LDFLAGS)" -o bin/zke-agent ./cmd/zke-agent

.PHONY: version
version:
	@echo $(VERSION)

.PHONY: generate-agent-protocol
generate-agent-protocol:
	./hack/generate-agent-protocol.sh

.PHONY: lint-agent-protocol
lint-agent-protocol:
	./hack/run-buf.sh lint

.PHONY: check-agent-protocol
check-agent-protocol: lint-agent-protocol
	./hack/check-agent-protocol.sh

.PHONY: breaking-agent-protocol
breaking-agent-protocol:
	@test -n "$(AGAINST)" || { echo "set AGAINST to a Git reference"; exit 1; }
	./hack/run-buf.sh breaking --against ".git#branch=$(AGAINST)"
