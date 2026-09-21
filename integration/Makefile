.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: help test e2e-test e2e-fixtures e2e-vet e2e-lint

help:
	@printf '%s\n' \
	  'make test          Run real l2geth client tests with the race detector' \
	  'make e2e-test      Run Docker Compose Anvil E2E tests with the race detector' \
	  'make e2e-fixtures  Regenerate Solidity fixtures using the pinned Docker image' \
	  'make e2e-vet       Run go vet on the tagged E2E package' \
	  'make e2e-lint      Run golangci-lint on the tagged E2E package'

e2e-test:
	L1DTL_E2E_GO="$(GO)" $(GO) test -tags=e2e -race -count=1 -timeout=15m -v ./e2e

e2e-fixtures:
	$(GO) run ./e2e/testdata/generate.go

e2e-vet:
	$(GO) vet -tags=e2e ./e2e

e2e-lint:
	$(GOLANGCI_LINT) run --build-tags=e2e ./e2e

test:
	$(GO) test -race ./...
