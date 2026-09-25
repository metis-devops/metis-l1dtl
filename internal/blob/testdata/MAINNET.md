# Captured mainnet Blob corpus

All captures target Inbox `0xff00000000000000000000000000000000001088` on
Ethereum mainnet and decode Metis L2 chain 1088. Data was retrieved on
2026-09-23 from `https://ethereum-rpc.publicnode.com` and
`https://ethereum-beacon-api.publicnode.com`.

Each row is an Inbox commitment and the Blob transaction it references, not two
independent Blob examples. The corpus contains six such pairs: six commitments,
six type-3 transactions, and six Blobs. An initial inspection of 13 commitments
across September 19–23 found only single-transaction, single-Blob batches; this
corpus does not claim real multi-Blob or multi-transaction-channel coverage.
Synthetic unit tests retain those separate structural scenarios.

| Batch | Commitment UTC | L1 block | L2 block range | Blocks | Transactions | Deposits |
| --- | --- | --- | --- | ---: | ---: | ---: |
| [65561](mainnet-20260923-batch-65561/manifest.json) | 2026-09-19 19:49:47 | 26013753 | 23168522–23168682 | 161 | 294 | 0 |
| [65657](mainnet-20260923-batch-65657/manifest.json) | 2026-09-22 06:38:11 | 26031289 | 23180351–23180427 | 77 | 166 | 0 |
| [65702](mainnet-20260923-batch-65702/manifest.json) | 2026-09-23 08:45:35 | 26039073 | 23185258–23185407 | 150 | 201 | 0 |
| [65709](mainnet-20260923-batch-65709/manifest.json) | 2026-09-23 12:34:47 | 26040211 | 23185979–23186086 | 108 | 167 | 0 |
| [65711](mainnet-20260923/manifest.json) | 2026-09-23 13:39:59 | 26040533 | 23186213–23186343 | 131 | 244 | 1 |
| [65712](mainnet-20260923-batch-65712/manifest.json) | 2026-09-23 14:17:35 | 26040721 | 23186344–23186489 | 146 | 289 | 0 |

Total: **773 L2 blocks, 1361 transactions, including one deposit**.

## Transaction references

- Batch 65561: [Inbox commitment](https://etherscan.io/tx/0xc6e2c6348fd1e5a0a96130ac4b96b6972dfbeec12d3930274dff170ee90c93f0) → [Blob transaction](https://etherscan.io/tx/0x4b50425a2c9d561eb0fe5930152d35232c0a2e29c4f5eef85eba13d947daa513).
- Batch 65657: [Inbox commitment](https://etherscan.io/tx/0xd6743a6e50de462dc3c0645624d1050861338c152277b3c88e1e9c83e0b400ea) → [Blob transaction](https://etherscan.io/tx/0x3dce2977fb8c5ab32dab759b7285e4ff3c8134a02b999a60963ff32cf0050b38).
- Batch 65702: [Inbox commitment](https://etherscan.io/tx/0x5d64842f017547dd6d0b282cb3b836711c7a0c54b9d2636a3d5ae7f2de240e16) → [Blob transaction](https://etherscan.io/tx/0xff26f39e5a0f75c9c520e0c9f5c86cd82dfcb8bb8f7d05cf6cd431a1f2b3fe8d).
- Batch 65709: [Inbox commitment](https://etherscan.io/tx/0x1ca4a16c61820488a4d1af028045f7c71b123f037b7cfe344e4f63f435d66e6f) → [Blob transaction](https://etherscan.io/tx/0xc54851a9780afa5bd6ceb64f313611061329b725455d61b30d3a4a6342c8d7d3).
- Batch 65711: [Inbox commitment](https://etherscan.io/tx/0xf138b0f7573c99a45c6ec070ecf688555a89315b77f96b682415d1d83e5e1e3e) → [Blob transaction](https://etherscan.io/tx/0xa0412e560ddc98f17b2f251ae14e49dcb1b7545d306cb9532ee5f83c83b25db7).
- Batch 65712: [Inbox commitment](https://etherscan.io/tx/0xc4cf3f5067e9fdf7e2cdc0008ff792c5bf180eaf38e7f6d9301eba94e118b922) → [Blob transaction](https://etherscan.io/tx/0xe7c9ece304d5f7da7ab581294f2ea582dd59e713d43f47bed3cd8a4cd286239f).

## Files and verification

Each directory preserves the execution transaction, receipt and containing header,
Beacon genesis/spec and a losslessly compressed Blob response. Its manifest pins
retrieval time, source endpoints, slot, versioned hashes, expected output counts,
and SHA-256 checksums. The original fixture uses a single-source manifest; newer
fixtures use a `sources` array so tooling can also represent future multi-source
samples. Unrelated block transaction hash lists are omitted from captures.

The copied TypeScript oracle runs offline, with no mvm checkout dependency:

```sh
npm ci --ignore-scripts --prefix internal/blob/testdata
node internal/blob/testdata/verify-mainnet.cjs
# Optional fixture directory names, relative to internal/blob/testdata:
node internal/blob/testdata/verify-mainnet.cjs mainnet-20260923-batch-65712
```

The default processes every `mainnet-*` directory containing a manifest. Existing
pinned summary counts must match before outputs are replaced. The Go tests verify
capture checksums, signed transaction/header provenance, Inbox references, KZG
versioned hashes, and every decoded field against the TypeScript result. Only the
known missing `0x` prefix on upstream deposit origins is normalized. Integration
tests request every block through the pinned real RollupClient and check the tip.
The corpus does not include CTC enqueue evidence: its one deposit-containing block
therefore returns null at the HTTP layer until that enqueue is ingested. Deposit
hydration and client transaction construction are covered by deterministic fixtures.

All six captured execution headers omit `slotNumber`, so these real samples
exercise the genesis/spec fallback. Header-slot priority remains unit-tested;
this corpus does not establish live Amsterdam-header compatibility. Tests replay
captured evidence offline: this is not live sender-event synchronization, L2
state replay, continuous operation, or full-node E2E validation.
