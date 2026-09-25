// Reproduce the copied TypeScript DTL output for captured mainnet fixtures.
// npm ci --ignore-scripts --prefix internal/blob/testdata
// node internal/blob/testdata/verify-mainnet.cjs [fixture-directory ...]
const path = require('path');
const fs = require('fs');
const zlib = require('zlib');
const crypto = require('crypto');
require('./register.cjs');
const { Blob } = require('./reference/blob');
const { handleEventsSequencerBatchInbox } = require('./reference/inbox');

function verify(dir) {
  const read = (file) => JSON.parse(fs.readFileSync(path.join(dir, file)));
  const manifest = read('manifest.json');
  const commit = read('commitment.json');
  const descriptors = manifest.sources || [{
    transactionFile: 'blob-transaction.json',
    beaconFile: 'beacon-blobs.json.gz',
    slot: manifest.slot,
  }];
  const sources = descriptors.map((entry) => ({
    ...read(entry.transactionFile),
    blobs: JSON.parse(zlib.gunzipSync(fs.readFileSync(path.join(dir, entry.beaconFile)))).data,
  }));
  const input = Buffer.from(commit.transaction.input.slice(2), 'hex');
  const extra = {
    batchIndex: Number(BigInt('0x' + input.subarray(2, 34).toString('hex'))),
    batchRoot: commit.block.parentHash,
    batchSize: input.readUInt32BE(66),
    prevTotalElements: Number(BigInt('0x' + input.subarray(34, 66).toString('hex'))) - 1,
    batchExtraData: '',
    blockNumber: Number(BigInt(commit.block.number)),
    timestamp: Number(BigInt(commit.block.timestamp)),
    submitter: commit.transaction.from,
    l1TransactionHash: commit.transaction.hash,
    l1TransactionData: commit.transaction.input,
    context: { inboxBlobSenderAddress: sources[0].transaction.from },
  };
  const receipts = new Map();
  const blocks = new Map();
  for (const source of sources) {
    const tx = { ...source.transaction, type: Number(BigInt(source.transaction.type)), index: Number(BigInt(source.transaction.transactionIndex)) };
    receipts.set(tx.hash, { ...source.receipt, blockNumber: Number(BigInt(source.receipt.blockNumber)), index: tx.index });
    const block = blocks.get(source.block.hash) || { timestamp: Number(BigInt(source.block.timestamp)), prefetchedTransactions: [] };
    block.prefetchedTransactions[tx.index] = tx;
    blocks.set(source.block.hash, block);
  }
  const required = (map, key) => {
    if (!map.has(key)) throw new Error(`Missing captured evidence: ${key}`);
    return map.get(key);
  };
  const options = {
    l2ChainId: manifest.l2ChainId,
    batchInboxAddress: commit.transaction.to,
    l1RpcProvider: {
      getBlockNumber: async () => extra.blockNumber,
      getTransactionReceipt: async (hash) => required(receipts, hash),
      getBlock: async (hash) => required(blocks, hash),
    },
    l1BeaconProvider: {
      getBlobs: async (timestamp, hashes) => {
        const source = sources.find((s) => Number(BigInt(s.block.timestamp)) === timestamp && JSON.stringify(s.transaction.blobVersionedHashes) === JSON.stringify(hashes));
        if (!source) throw new Error('Missing captured Blob response');
        return source.blobs.map((raw) => new Blob(raw).toData());
      },
    },
  };
  return handleEventsSequencerBatchInbox.parseEvent({}, extra, manifest.l2ChainId, manifest.l1ChainId, options).then((result) => {
    const oracle = zlib.gzipSync(JSON.stringify(result));
    const decoded = result.blockEntries;
    const expected = {
      batchIndex: extra.batchIndex,
      firstL2Block: Math.min(...decoded.map((b) => b.index + 1)),
      lastL2Block: Math.max(...decoded.map((b) => b.index + 1)),
      blockCount: decoded.length,
      transactionCount: decoded.reduce((n, b) => n + b.transactions.length, 0),
      depositCount: decoded.flatMap((b) => b.transactions).filter((tx) => tx.queueOrigin === 'l1').length,
    };
    if (!decoded.length) throw new Error('Captured batch decoded no blocks');
    if (manifest.expected && JSON.stringify(manifest.expected) !== JSON.stringify(expected)) {
      throw new Error(`Oracle differs from pinned summary in ${dir}`);
    }
    fs.writeFileSync(path.join(dir, 'typescript-blocks.json.gz'), oracle);
    manifest.sha256['typescript-blocks.json.gz'] = crypto.createHash('sha256').update(oracle).digest('hex');
    manifest.expected = expected;
    fs.writeFileSync(path.join(dir, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
    console.log({ fixture: path.basename(dir), blobTransactions: sources.length, blobs: sources.reduce((n, s) => n + s.blobs.length, 0), ...expected });
  });
}

const directories = process.argv.length > 2
  ? process.argv.slice(2).map((dir) => path.resolve(__dirname, dir))
  : fs.readdirSync(__dirname).filter((name) => name.startsWith('mainnet-') && fs.existsSync(path.join(__dirname, name, 'manifest.json'))).sort().map((name) => path.join(__dirname, name));
(async () => { for (const dir of directories) await verify(dir); })().catch((err) => { console.error(err); process.exitCode = 1; });
