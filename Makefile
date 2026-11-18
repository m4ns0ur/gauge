GO ?= go
BIN := bin/gauge
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/m4ns0ur/gauge/internal/version.Version=$(VERSION) \
	-X github.com/m4ns0ur/gauge/internal/version.Commit=$(COMMIT) \
	-X github.com/m4ns0ur/gauge/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: all build install test vet fmt check tidy clean run-smoke

all: check build

# Build the local binary and install `gauge` into GOBIN (or GOPATH/bin) so it
# is available on PATH.
build: install
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gauge

# Install only. Uses the same build identity as `make build`.
install:
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/gauge
	@dir="$$($(GO) env GOBIN)"; if [ -z "$$dir" ]; then dir="$$($(GO) env GOPATH)/bin"; fi; echo "installed: $$dir/gauge"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal

check:
	@files="$$(gofmt -l cmd internal)"; if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi
	$(GO) vet ./...
	$(GO) build ./...
	$(GO) test ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin

# Demonstrates the full local lifecycle with the fake adapter.
run-smoke: build
	./$(BIN) run --config examples/gauge.toml --suite smoke --agent fake
