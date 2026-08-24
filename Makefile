GO ?= go
GOFMT ?= $(dir $(shell command -v $(GO)))gofmt
GOFLAGS ?=
CGO_ENABLED ?= 0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X mihomoctl/internal/domain.Version=$(VERSION) -X mihomoctl/internal/domain.Commit=$(COMMIT) -X mihomoctl/internal/domain.Date=$(BUILD_DATE)

.PHONY: all build test test-race check fmt install snapshot release clean

all: build

build:
	mkdir -p dist
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o dist/mihomoctl ./cmd/mihomoctl

test:
	$(GO) test ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

check:
	@set -e; unformatted="$$(find cmd internal -type f -name '*.go' -exec $(GOFMT) -l {} +)"; \
		test -z "$$unformatted" || { printf 'gofmt required:\n%s\n' "$$unformatted" >&2; exit 1; }
	$(GO) vet ./...
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

install:
	GO_BIN=$(GO) ./scripts/install.sh

snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean

clean:
	rm -rf -- dist
