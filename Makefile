# ZKE development commands.
#
# These wrap the tools the repository already relies on so that a contributor
# and an automated check run exactly the same thing.

GO ?= go
GOLANGCI_LINT ?= golangci-lint

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
	$(GO) build ./...

.PHONY: generate-agent-protocol
generate-agent-protocol:
	./hack/generate-agent-protocol.sh
