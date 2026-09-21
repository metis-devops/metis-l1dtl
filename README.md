# metis-l1dtl

A Go L1 deposit ingestion service for the existing Metis `l2geth` sequencer.
Storage uses Pebble v2. It implements the DTL HTTP behavior needed when batch ingestion is disabled.
It is **not** an L2 history recovery service or an L1 verifier backend.

## Build and run

Requires Go 1.27.1. Run from this directory:

```sh
make build
./bin/metis-l1dtl \
  --l1-rpc=https://YOUR_L1_RPC \
  --l1-chain-id=1 \
  --l2-chain-id=1088 \
  --address-manager=YOUR_ADDRESS_MANAGER \
  --l1-start-height=CTC_DEPLOYMENT_BLOCK \
  --db=data \
  --listen=0.0.0.0:7878
```

See `config.example.sh`. All configuration uses CLI flags; there is no implicit
network preset, environment override, or automatic deployment-height discovery.
Required flags are the RPC URL, both chain IDs, AddressManager and inclusive CTC
deployment height. Defaults: 35 confirmations, 5s polling, 2,000 blocks per scan,
`data`, `0.0.0.0:7878`.

The RPC must support historical logs and headers. Historical state (archive
`eth_call` / `eth_getCode`) is not required. On an empty database, initialization
searches backward from deployment height minus one for the most recent CTC
`AddressSet` event, in pages of at most `--batch-size` blocks. It may query logs
back to genesis if no earlier event exists; a node that has pruned those logs
cannot supply this history. Initialization trusts event block hashes from the RPC
and checks the hash of the block immediately before the scan start before and
after each page and before the first deposit commit. It does not fetch event
block headers. A start height of zero needs no initialization anchor.
Subsequent changes are applied in log order, including within a block.

Initialization does not advance the deposit checkpoint or readiness. Each page
has the scan's five-minute timeout; transient failures retry the current page.
Completed pages are remembered in memory, so a restart before the first commit
repeats initialization. Existing checkpoints reuse the stored CTC address.
Start early enough to include queue index 0. Starting only at Inbox activation
can lose earlier deposits.

The process validates the L1 chain ID and AddressManager code on startup. Each
instance serves one L2 chain ID and holds an exclusive database lock. Database
identity includes the schema version, both chain IDs, manager and start height.
Changing these requires a separate database. `--db` is a Pebble directory (the
default is `data`), not a single file. Existing bbolt files and
TypeScript DTL databases cannot be opened or migrated by this service; use a new
directory and resynchronize. Existing files are rejected without replacement.

## Connecting l2geth

Use this HTTP URL as the sequencer's existing rollup client URL and select the
`l1` backend. No l2geth code changes are required. Preserve the sequencer's complete
L2 state and correct enqueue cursor when replacing its DTL. This service cannot
reconstruct missing L2 state or determine which deposits have already been
included: every `ctcIndex` is null. A missing enqueue cursor makes the current
client search backward through the deposit history for confirmations; none will
be found. Do not rely on that search to recover the cursor of an existing chain.

Start the service, wait for `/readyz` to return 200, then connect the sequencer.
The first synchronization scans from the configured start through
`max(0, L1 tip - confirmations)`. The service trusts the RPC's log fork provenance;
it does not fetch every block header, verify parent continuity, or bind log
hashes to headers. It checks the window end hash before fetching logs and before
committing, and rechecks an existing checkpoint before and after scanning.
A committed scan window with an existing checkpoint makes five header requests
regardless of its size: one tip request, two checkpoint checks and two end checks.
Initialization adds a fixed number of anchor checks per page. Stable anchors do
not detect logs returned from another fork; this RPC consistency risk is accepted.
Log ranges, removed flags, topics, ABI, address transitions, chain IDs and queue
continuity are still validated. Deposits, CTC address and checkpoint commit
atomically, including empty scan ranges.

The former `--rpc-batch-size` flag has been removed; remove it from existing
startup scripts. `--batch-size` still controls the block range per log query.
RPC calls in a scan share a five-minute timeout. Reduce `--batch-size` for slow
providers. Transient RPC errors retry without advancing the checkpoint.

## HTTP contract

All data routes use GET. The default backend is `l1`; an explicitly supplied
backend must be exactly `l1`. Bad parameters and other chain IDs return 400.
Unimplemented routes return 404. Operational failures return 503.

| Route | Result |
| --- | --- |
| `/enqueue/latest/:chainId` | Latest deposit |
| `/enqueue/index/:index/:chainId` | Deposit by queue index |
| `/eth/context/latest` | Confirmed L1 block number/hash, current Unix timestamp (legacy behavior) |
| `/eth/context/blocknumber/:number` | Confirmed block context with actual block timestamp |
| `/highest/l1` | Last fully committed L1 scan height; null before first commit |
| `/eth/syncing/:chainId` | `syncing` and `currentTransactionIndex: 0`; no fabricated L2 tip |
| `/transaction/latest/:chainId` | `{ "transaction": null, "batch": null }` |
| `/transaction/index/:index/:chainId` | Same empty transaction result |
| `/block/latest/:chainId` | `{ "block": null, "batch": null }` |
| `/block/index/:index/:chainId` | Same empty block result |
| `/healthz` | Process liveness |
| `/readyz` | 200 once caught up; 503 while behind or halted |

Missing deposits return all eight response fields as null. Existing deposits use
JSON integer indices/timestamps, hex data/addresses and a decimal string gas limit;
`ctcIndex` is always null. Requests for blocks beyond the confirmation boundary
return null context fields. Integer inputs and event values are range checked.

There is no CTC/Inbox batch decoding, MinIO/Blob retrieval, state-root/verifier
API, L2 ingestion, database migration, authentication or multi-chain hosting.
Keep the listener on a trusted network.

## Persistence and failures

Each scan atomically commits deposits, the active CTC address and the checkpoint
using a Pebble indexed batch with a synchronous WAL commit. Checkpoint comparison
and writes are serialized; latest-index and deposit reads see one committed state.
Missing records at or below the committed latest queue index are integrity errors;
only genuinely absent deposits return the legacy null response.
Duplicate logs are idempotent; conflicting records, missing queue indices,
malformed relevant events and changed checkpoint/window/initialization anchors
halt synchronization.
Fatal errors detected by HTTP storage reads also cancel ingestion. Fatal status
publication and database commits are serialized: an in-flight commit finishes
before the halt is published, and no later commit can start. Readiness is withdrawn
as soon as a scan discovers that the checkpoint is behind the confirmed tip.
Before committing, the end block and prior checkpoint are checked again.
Committed checkpoints are also checked on restart and during idle polling.

An integrity failure leaves the process alive for diagnostics but makes all data
routes and readiness return 503. There is no automatic reorg rollback. Inspect
the JSON error logs and RPC data first. After a genuine committed-chain reorg,
stop the service, retain the old database for investigation and synchronize into
a new database. Restarting on the same inconsistent checkpoint fails again.
Unconfirmed fork changes detected during a scan are also treated conservatively
as integrity failures. SIGINT/SIGTERM stops ingestion, drains HTTP requests and
closes the database.

## Tests

```sh
make test
make test-race
make vet
make build
```

Run `make ci` for all checks above plus the integration race tests. `make help`
lists all targets; `make lint` uses an installed `golangci-lint` and the repository
configuration. `make clean` removes `bin/metis-l1dtl`. Builds enable CGO by default;
builds and race tests require a working C toolchain. Use `make build CGO_ENABLED=0`
to opt out for a build. Docker builds also default to CGO, include a C toolchain,
and accept `--build-arg CGO_ENABLED=0`. Override `GO` or `GOLANGCI_LINT` when
using a non-default tool executable.

The integration module calls the **real** l2geth RollupClient against an
HTTP test server. It covers deposit conversion, startup status/context requests,
empty transaction/block tips and the unconfirmed enqueue path:

```sh
make integration-test
```

It uses the l2geth version pinned in `integration/go.mod`. To test a local
checkout, run from `integration/`:
`go mod edit -replace github.com/MetisProtocol/mvm/l2geth=/absolute/path/to/l2geth`.
Production builds do not depend on that checkout. The default test targets use
deterministic local RPC doubles.

### Anvil E2E

```sh
make e2e-test
make ci-e2e       # Existing ci checks plus E2E
make e2e-vet
make e2e-lint
```

E2E recipes live in `integration/Makefile`; the root targets delegate to them.
They can also run directly with `make -C integration e2e-test` (and similarly
for `e2e-fixtures`, `e2e-vet`, and `e2e-lint`). Command-line `GO` and
`GOLANGCI_LINT` overrides propagate through the root targets.

Requires a running local Docker Engine (Docker Desktop works), Docker Compose,
Go and a C toolchain for the race detector. No host Anvil/Forge installation is
needed. The first run may download `ghcr.io/foundry-rs/foundry:v1.7.1`; the image
supports amd64 and arm64. `make ci` and `make integration-test` do not run E2E.
Missing Docker/Compose or a failed container startup fails E2E instead of skipping it.

The Go harness manages `compose.e2e.yaml` automatically. Each scenario gets a
unique Compose project, a fresh in-memory Anvil chain (31337, Cancun), a random
RPC port bound to `127.0.0.1`, and a temporary Pebble database. It builds the real
service once with `-race`, runs it on the host, and uses the real l2geth
RollupClient against its HTTP listener. Both test and service use the `GO`
Makefile override. E2E disables Go's test-result cache.

Coverage includes historical AddressSet discovery and deployment-start scanning,
RPC batching, chain filtering, HTTP field/null compatibility, confirmation depth,
same-block CTC changes, empty-block progress, SIGTERM/restart recovery, atomic
rejection of queue gaps/conflicts, and fail-closed committed-chain reorgs.
Transactions are mined by Anvil; the tests do not inject synthetic RPC logs.
On failure, container and service logs are printed. Cleanup stops the service and
runs project-scoped `docker compose down --volumes --remove-orphans`, then checks
that no project containers remain. After a forcibly killed test runner, use
`docker compose ls -a` to locate its `l1dtl-e2e-*` project and remove only that
project with `docker compose -f compose.e2e.yaml -p PROJECT down --volumes --remove-orphans`.

Minimal Solidity test contracts and their ABI/deployment bytecode live in
`integration/e2e/testdata`. They model the event interface, not production Metis
contract logic. Regenerate artifacts after changing Solidity with
`make e2e-fixtures` (Dockerized Forge, Solidity 0.8.28, Cancun, optimizer disabled,
no bytecode metadata; compiler download may require network access).
The harness checks the source hash and event ABI before deployment.

Passing E2E validates a local Anvil container, fixture contracts, the host service
and the real client together. It does not validate production Metis contracts, the
service Docker image, live networks, or a complete L2 node.

## Container

The binary includes a standalone probe requiring no service flags, RPC access,
database access, shell or curl:

```sh
metis-l1dtl healthcheck
metis-l1dtl healthcheck --url=http://127.0.0.1:7878/readyz --timeout=3s
```

Defaults are `--url=http://127.0.0.1:7878/healthz` and `--timeout=3s`.
HTTP 200 exits 0 silently; other statuses, connection errors, timeouts and invalid
arguments exit 1 with a diagnostic on stderr. Redirects are not followed and
proxy environment variables are ignored. `healthcheck --help` exits 0.
Set `--url` explicitly when using a different listen address or port.

The Docker image checks liveness every 30s, with a 5s Docker timeout, a 10s
start period and three retries. Override the command for a custom listener with
`--health-cmd='metis-l1dtl healthcheck --url=http://127.0.0.1:8080/healthz'`.
To have Docker health represent synchronization readiness instead, override the
URL to `/readyz`.

Kubernetes exec probes can use the same binary (container spec fragment):

```yaml
livenessProbe:
  exec:
    command: ["/usr/local/bin/metis-l1dtl", "healthcheck"]
  timeoutSeconds: 5
  periodSeconds: 30
  failureThreshold: 3
readinessProbe:
  exec:
    command: ["/usr/local/bin/metis-l1dtl", "healthcheck", "--url=http://127.0.0.1:7878/readyz"]
  timeoutSeconds: 5
  periodSeconds: 10
  failureThreshold: 1
```

Liveness remains successful during catch-up or an integrity halt; readiness
fails in those states. Use `/readyz` for traffic admission and `/healthz` for
restart decisions. Kubernetes HTTP probes may also target these routes directly.

```sh
docker build -t metis-l1dtl .
docker run --rm -p 127.0.0.1:7878:7878 -v l1dtl-data:/data metis-l1dtl \
  --l1-rpc=https://YOUR_L1_RPC --l1-chain-id=1 --l2-chain-id=1088 \
  --address-manager=YOUR_ADDRESS_MANAGER --l1-start-height=CTC_DEPLOYMENT_BLOCK \
  --db=/data --listen=0.0.0.0:7878
```
