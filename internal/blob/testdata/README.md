# TypeScript oracle fixture

`typescript-blob.hex` contains a deterministic Metis span batch with two blocks:
one signed sequencer transaction and one deposit. The test signing key is the
public, synthetic repeating `01` key in `generate.cjs`; it is not a credential.

`typescript-blocks.json` is the unmodified output of the original TypeScript
`handleEventsSequencerBatchInbox.parseEvent` for this Blob. Regenerate with:

```
npm ci --ignore-scripts --prefix internal/blob/testdata
node internal/blob/testdata/generate.cjs
```

Regeneration uses the copied TypeScript reference in `reference/` and this
directory's exact npm dependencies and lockfile. It does not load any sibling
checkout. Production builds and normal Go tests use the committed fixture and
need no Node.js or TypeScript installation. See `reference/SOURCES.json` for
upstream revision, source hashes and the limited adaptations, and
`reference/LICENSE` for the upstream MIT license. The existing upstream `l1BlobData` example does not decode with
the current upstream span decoder (`uvarint overflows a 64-bit integer`), so this
fixture constructs known signed payloads and obtains expected output by executing
the current handler. Both zlib and Brotli decode paths are tested.

The fixture retains a known upstream formatting defect: deposit `origin` lacks
`0x`. The golden test explicitly normalizes only this prefix; production emits a
standard address, verified by the real RollupClient integration test. The fixture
does not assert live-network, production-contract or full-node compatibility.

## Captured Ethereum mainnet corpus

Six `mainnet-*` directories contain real Blob transactions and their referring
Inbox submissions, retrieved from public execution/Beacon endpoints on
2026-09-23. They cover 773 L2 blocks and 1361 transactions, including one deposit.
See [MAINNET.md](MAINNET.md) for the sample table, transaction links, formats,
verification commands and evidence limits. The verifier discovers all captures
by default; Go and real RollupClient tests exercise each one offline.
