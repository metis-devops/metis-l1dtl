# Ethereum mainnet Blob capture, 2026-09-23

At retrieval, the latest two transactions for Inbox
`0xff00000000000000000000000000000000001088` were:

| Role | Transaction | L1 block |
| --- | --- | --- |
| DA=3 Inbox commitment | [0xf138b0f7573c99a45c6ec070ecf688555a89315b77f96b682415d1d83e5e1e3e](https://etherscan.io/tx/0xf138b0f7573c99a45c6ec070ecf688555a89315b77f96b682415d1d83e5e1e3e) | 26040533 |
| Referenced type-3 Blob transaction | [0xa0412e560ddc98f17b2f251ae14e49dcb1b7545d306cb9532ee5f83c83b25db7](https://etherscan.io/tx/0xa0412e560ddc98f17b2f251ae14e49dcb1b7545d306cb9532ee5f83c83b25db7) | 26040528 |

Execution data came from `https://ethereum-rpc.publicnode.com` via
`eth_getTransactionByHash`, `eth_getTransactionReceipt`, and `eth_getBlockByHash`.
The capture preserves their transaction, receipt and header values; unrelated
transaction hash lists were omitted from the block objects. Individual fetch
UTC timestamps are included in those files.

The raw 131072-byte Blob was retrieved at approximately **13:55:45 UTC** from
`https://ethereum-beacon-api.publicnode.com/eth/v1/beacon/blobs/15278893`, filtered
by versioned hash
`0x01513897060a1d2a3c2c70a2f21ec3c1c56c81b1e843233b327b9d6afdb49371`.
`beacon-blobs.json.gz` is a losslessly compressed JSON response. The captured
execution header has no `slotNumber`; the captured genesis/spec values resolve
slot **15278893** from its timestamp. This sample does not exercise a live
Amsterdam header; direct header-slot selection remains covered by unit tests.

The Go Beacon client recomputes the KZG commitment/versioned hash. Decoding gives
**batch 65711**, **131 L2 blocks (23186213–23186343)**, and **244 transactions**,
including **one deposit**. `typescript-blocks.json.gz` is the original TypeScript
Inbox handler output for the captured data, produced with:

```sh
npm ci --ignore-scripts --prefix internal/blob/testdata
node internal/blob/testdata/verify-mainnet.cjs
```

The upstream TypeScript source revision and fixture checksums are recorded in
`manifest.json`. The decoder is copied locally under `../reference/`, with source
and vendored hashes in `../reference/SOURCES.json` and the upstream MIT license.
The script uses this fixture package's pinned npm dependencies and never loads
the mvm checkout.
The golden test compares every decoded block/transaction field, normalizing only
the known missing `0x` prefix on TypeScript deposit origins. The integration test
reads all 131 blocks with the pinned real RollupClient through local HTTP/Pebble.

This is evidence of successful retrieval and decoding of an actual mainnet Blob,
and offline client compatibility for this sample. It does **not** prove live
sender-event history scanning, continuous service synchronization, replay against
L2 state, or full-node operation. Tests make no network calls and are unaffected
by later Beacon pruning. Addresses and signatures are public chain data; no
private RPC credentials or signing keys are stored here.
