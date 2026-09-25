# Local TypeScript Blob oracle

This directory copies the decoding path from Metis DTL at the revision recorded
in `SOURCES.json`. It is an independent reference for the Go decoder, used only
by the optional fixture-generation scripts. There are no runtime imports from
the mvm repository, `@metis.io/core-utils`, or native `c-kzg`.

The files retain upstream parsing behavior, including the known missing `0x`
prefix on deposit origins. The Go golden test handles that documented difference.
Do not change this oracle to make a Go decoder regression pass.

Adaptations are intentionally limited:

- `channel.ts` imports copied `QueueOrigin` and `L2Transaction` declarations.
- `blob.ts` retains unpacking and omits the unused native KZG method. Go tests
  separately verify the real Blob against its KZG versioned hash.
- `index.ts` retains fetch/frame/channel assembly, using injected offline provider
  interfaces instead of importing the Beacon HTTP implementation.
- `inbox.ts` extracts the DA=3 parsing, batch metadata and transaction mapping;
  other DA modes, MinIO, database writes and unused event lifecycle methods are
  omitted. It rejects inputs outside this offline Blob-only scope.
- Small hex/signature helpers and the required database types are copied locally.

`SOURCES.json` records the original path, original SHA-256, local SHA-256 and
adaptations for every copied file. Preserve the upstream MIT notice in `LICENSE`.
Dependencies belong to `../package.json` and `../package-lock.json`; install with
`npm ci --ignore-scripts --prefix internal/blob/testdata` from the repository root.
Normal Go tests and production builds do not install or execute this code.
