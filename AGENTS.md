# Repository guidance

## Purpose and scope

`metis-l1dtl` is a Go L1 deposit ingestion service for the existing Metis
`l2geth` sequencer, matching the DTL behavior with batch ingestion disabled.

- Serve one configured L2 chain ID per instance; reject other chain IDs.
- Scan deposits from the inclusive CTC deployment start, with queue indices
  contiguous from zero. Inbox activation is not the deposit history start.
- Keep CTC/Inbox batch decoding, MinIO/Blob retrieval, L2 ingestion, state-root
  and verifier APIs, and old database migration out of scope unless requested.
- Do not modify the sibling `mvm/l2geth` checkout to make compatibility tests pass.
  This service does not recover L2 history or a missing sequencer enqueue cursor.

## Code layout

- `cmd/metis-l1dtl`: process startup, RPC validation, lifecycle and shutdown.
- `internal/config`: CLI flags and validation. Keep defaults documented in
  `README.md` and `config.example.sh` consistent with the code.
- `internal/ingest`: historical AddressManager/CTC tracking, event decoding,
  confirmation boundaries, checkpoint validation and synchronization status.
- `internal/store`: Pebble v2 persistence, database identity and atomic commits.
- `internal/server`: HTTP compatibility responses and health/readiness routes.
- `integration`: separate Go module testing the real l2geth RollupClient.
  Run it with `make integration-test`; root module test targets do not include it.
- `integration/e2e`: opt-in `e2e` build-tag tests using Docker Compose Anvil,
  real service processes and the real RollupClient. Solidity fixtures and
  committed ABI/bytecode are in `testdata`; regenerate with `make e2e-fixtures`.

Use the Go version and dependencies declared in `go.mod`. Keep production builds
independent of the local l2geth checkout; its replacement belongs in the
integration module only.

## Persistence and ingestion invariants

- Use `github.com/cockroachdb/pebble/v2`. `--db` is a directory, even when its
  name ends in `.db`. Never overwrite or silently convert an existing bbolt file.
- Commit deposits, active CTC address and scan checkpoint in one synchronous
  WAL-backed batch. Empty scan ranges also advance the checkpoint atomically.
- Preserve serialized checkpoint comparison and writes. Latest-index and record
  reads must observe a consistent committed state. Close Pebble value handles,
  batches and iterators; do not retain borrowed bytes after releasing their owner.
- Keep duplicate events idempotent, including duplicates within one batch.
  Conflicting records, stale checkpoints, queue gaps and integer overflow must
  fail without publishing partial records or advancing the checkpoint.
- Validate database identity before reuse: schema version, L1/L2 chain IDs,
  AddressManager and start height. Do not infer or repair missing identity.
- Resolve historical CTC addresses and apply changes in event order, including
  changes within a block. Do not scan all history using only the current address.
- Trust RPC log fork provenance in both scanning and initial CTC discovery. Do
  not fetch per-block/event headers or verify parent continuity/log hash binding.
  Preserve window-end and checkpoint hash checks before/after scanning, plus the
  `Start-1` initialization anchor before/after each page and before first commit.
  Store anchor hashes by value. Stable anchors may accept logs from another fork.
- Preserve log range/Removed/topics checks, ABI validation, chain filtering and
  address/queue continuity. Retry transient RPC errors without advancing the
  checkpoint or publishing failed initialization-page progress.
- Integrity failures halt ingestion and make data/readiness routes unavailable.
  This includes failures discovered by HTTP reads. Serialize fatal publication
  with commits; missing records within the committed queue range are integrity
  failures, not normal null responses. Withdraw readiness when lag is detected.
  Do not add automatic rollback, skip malformed evidence or suppress errors to
  keep synchronization moving without an explicit scope change.

## HTTP compatibility

Treat `README.md`, the original `mvm/packages/data-transport-layer` implementation,
and the real `mvm/l2geth/rollup` client as compatibility references. Preserve
parameters, field types, null values and client error behavior, not just routes.

- `ctcIndex` stays null; missing deposits return all eight fields as null.
- Gas limits are decimal strings; indices and timestamps are JSON integers.
- Transaction/block lookup routes return the agreed empty objects.
- Latest L1 context uses current Unix time for its timestamp; numbered context
  uses the actual block timestamp. Do not silently normalize this legacy behavior.
- The default backend is `l1`; explicit unsupported backends return 400.
- Do not populate transaction-index fields with L1 scan heights.

## Validation and delivery

For code changes, run the checks relevant to the affected behavior using the
root Makefile. Before finishing storage, ingestion or HTTP changes, run:

```sh
make test
make test-race
make vet
make build
```

For storage or client-facing changes, also run from the repository root:

```sh
make integration-test
```

`make ci` runs all checks above, including integration race tests. `make build`
enables CGO by default and writes `bin/metis-l1dtl`; builds and race tests require
a working C toolchain. `make build CGO_ENABLED=0` opts out for a build. Docker
builds also default to CGO and include a C toolchain.
`make lint` runs the installed `golangci-lint` separately from `make ci`.
Use `make help` for all targets; `GO` and `GOLANGCI_LINT` can override tool paths.

`make e2e-test` requires local Docker Engine, Docker Compose and a C toolchain;
it manages `compose.e2e.yaml`, builds a race-enabled host service, and disables
test caching. Missing prerequisites must fail, not skip. `make ci-e2e` adds E2E
to `make ci`; the existing `ci` and integration targets remain Docker-independent.
Keep E2E recipes in `integration/Makefile`; root E2E targets only delegate with
`$(MAKE) -C integration`. Preserve direct module invocation and tool overrides.
For E2E changes also run `make e2e-vet` and `make e2e-lint`, repeat E2E three times
to check stability, and verify project containers are removed. Keep per-scenario
Compose projects, loopback-only dynamic ports, bounded polling and cleanup,
failure logs, and post-shutdown database checks for atomicity/reorg scenarios.
These tests use minimal contracts; do not describe them as production-contract,
service-container, live-network or full L2-node validation.

The integration module uses the l2geth version pinned in `integration/go.mod`.
For a local checkout, set a replacement only in the integration module as
documented in `README.md`. If the configured dependency is unavailable, report
the missing gate rather than replacing it with a mock client.
Use deterministic RPC fixtures for unit tests. Cover atomic rollback, duplicate
conflicts, checkpoint races, reopen/identity checks and same-block address changes
when modifying their triggering paths. Keep the process restart/shutdown tests.

Documentation-only changes need content and diff checks, not a full test rerun.
Keep generated binaries, databases and credentials out of commits. In delivery
notes, distinguish local tests, cross-compilation, Docker validation and actual
live-network/full-node E2E results; report checks that could not run.
