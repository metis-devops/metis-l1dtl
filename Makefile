.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint
CGO_ENABLED ?= 1
export CGO_ENABLED

.PHONY: help build test test-race vet integration-test e2e-test e2e-fixtures e2e-vet e2e-lint lint ci ci-e2e clean

help:
	@printf '%s\n' \
	  'make build             Build bin/metis-l1dtl with CGO enabled by default' \
	  'make test              Run root module tests' \
	  'make test-race         Run root module tests with the race detector' \
	  'make vet               Run go vet on the root module' \
	  'make integration-test  Run real l2geth client tests with the race detector' \
	  'make e2e-test          Run Docker Compose Anvil E2E tests with the race detector' \
	  'make e2e-fixtures      Regenerate Solidity fixtures using the pinned Docker image' \
	  'make e2e-vet           Run go vet on the tagged E2E package' \
	  'make e2e-lint          Run golangci-lint on the tagged E2E package' \
	  'make lint              Run the installed golangci-lint' \
	  'make ci                Run tests, race checks, vet, build and integration tests' \
	  'make ci-e2e            Run ci plus Docker Compose Anvil E2E tests' \
	  'make clean             Remove the generated service binary'

build:
	mkdir -p bin
	$(GO) build -o bin/metis-l1dtl ./cmd/metis-l1dtl

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

integration-test:
	$(MAKE) -C integration test

e2e-test e2e-fixtures e2e-vet e2e-lint:
	$(MAKE) -C integration $@

lint:
	$(GOLANGCI_LINT) run ./...

ci: test test-race vet build integration-test

ci-e2e: ci e2e-test

clean:
	rm -f bin/metis-l1dtl
