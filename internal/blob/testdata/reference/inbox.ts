// Blob-only extraction of the upstream Inbox parseEvent handler.
// See SOURCES.json and LICENSE for provenance and intentional omissions.
import { ethers, toNumber } from 'ethersv6'
import { fromHexString, remove0x, toHexString } from './hex-strings'
import { L2Transaction, QueueOrigin } from './types'
import { BlockEntry, DecodedSequencerBatchTransaction, TransactionBatchEntry } from './database-types'
import { parseSignatureVParam } from './eth-tx'
import { fetchBatches } from './index'

export const handleEventsSequencerBatchInbox = {
  parseEvent: async (_event: unknown, extraData: any, l2ChainId: number, l1ChainId: number, options: any) => {
    const blockEntries: BlockEntry[] = []
    const calldata = fromHexString(extraData.l1TransactionData)
    if (calldata.length < 70 || calldata[0] !== 3 || calldata[1] !== 0) {
      throw new Error('This offline oracle only accepts uncompressed DA=3 commitments')
    }
    const contextData = calldata.subarray(70)
    const channels = []
      if (contextData.length % 32 !== 0) {
        throw new Error(
          `Blob tx hashes length is not multiple of 32, data: ${contextData}`
        )
      }

      // fetch blobs from cl
      const blobTxHashes = []
      for (let i = 0; i < contextData.length; i += 32) {
        blobTxHashes.push(ethers.hexlify(contextData.subarray(i, i + 32)))
      }

      const result = await fetchBatches({
        blobTxHashes,
        chainId: toNumber(l1ChainId),
        l2ChainId: options.l2ChainId,
        batchInbox: options.batchInboxAddress,
        batchSenders:
          extraData.context && extraData.context.inboxBlobSenderAddress
            ? [extraData.context.inboxBlobSenderAddress.toLowerCase()]
            : [],
        concurrentRequests: 0,
        l1RpcProvider: options.l1RpcProvider,
        l1BeaconProvider: options.l1BeaconProvider,
      })
      channels.push(...result)
    const transactionBatchEntry: TransactionBatchEntry = {
      index: toNumber(extraData.batchIndex),
      root: extraData.batchRoot,
      size: toNumber(extraData.batchSize),
      prevTotalElements: toNumber(extraData.prevTotalElements),
      extraData: extraData.batchExtraData,
      blockNumber: extraData.blockNumber,
      timestamp: extraData.timestamp,
      submitter: extraData.submitter,
      l1TransactionHash: extraData.l1TransactionHash,
    }

      // when using blob data, the context data is not in the old Metis format,
      // it is chunked by optimism frames, so we need to parse it differently
      // TODO: async parse the channels
      for (const channel of channels) {
        // since currently we can only handle span batch,
        // so we can just skip the singular batch
        for (const spanBatch of channel.batches) {
          for (let j = 0; j < spanBatch.batches.length; j++) {
            const l2BlockNumber = spanBatch.l2StartBlock + j
            const batchElement = spanBatch.batches[j]
            blockEntries.push({
              index: l2BlockNumber - 1,
              batchIndex: Number(extraData.batchIndex),
              timestamp: batchElement.timestamp,
              extraData: batchElement.extraData,
              transactions: batchElement.transactions.map(
                (tx: L2Transaction) => {
                  const isSequencerTx = tx.queueOrigin === QueueOrigin.Sequencer
                  // decode raw tx
                  return {
                    index: l2BlockNumber - 1,
                    batchIndex: Number(extraData.batchIndex),
                    blockNumber: tx.l1BlockNumber,
                    timestamp: batchElement.timestamp,
                    gasLimit: tx.gasLimit.toString(10),
                    target: ethers.ZeroAddress,
                    origin: isSequencerTx ? ethers.ZeroAddress : tx.l1TxOrigin,
                    data: isSequencerTx ? tx.rawTransaction : tx.data,
                    queueOrigin: isSequencerTx ? 'sequencer' : 'l1',
                    value: isSequencerTx ? '0x' + tx.value.toString(16) : '0x0',
                    queueIndex: isSequencerTx ? null : tx.nonce,
                    decoded: isSequencerTx
                      ? decodeSequencerBatchTransaction(
                          Buffer.from(remove0x(tx.rawTransaction), 'hex'),
                          l2ChainId
                        )
                      : null,
                    confirmed: true,
                    seqSign: isSequencerTx
                      ? `0x${removeLeadingZeros(
                          remove0x(tx.seqR)
                        )},0x${removeLeadingZeros(
                          remove0x(tx.seqS)
                        )},0x${removeLeadingZeros(remove0x(tx.seqV))}`
                      : null,
                  }
                }
              ),
              confirmed: true,
            })
          }
        }
      }

      return {
        transactionBatchEntry,
        blockEntries,
      }
  },
}

const decodeSequencerBatchTransaction = (
  transaction: Buffer,
  l2ChainId: number
): DecodedSequencerBatchTransaction => {
  const decodedTx = ethers.Transaction.from(`0x${transaction.toString('hex')}`)

  return {
    nonce: decodedTx.nonce.toString(),
    gasPrice: decodedTx.gasPrice.toString(),
    gasLimit: decodedTx.gasLimit.toString(),
    value: '0x' + decodedTx.value.toString(16),
    target: decodedTx.to ? toHexString(decodedTx.to) : null,
    data: toHexString(decodedTx.data),
    sig: {
      v: parseSignatureVParam(
        decodedTx.signature.networkV || decodedTx.signature.v,
        l2ChainId
      ),
      r: toHexString(decodedTx.signature.r),
      s: toHexString(decodedTx.signature.s),
    },
  }
}

const removeLeadingZeros = (inputString: string): string => {
  const trimmedString = inputString.replace(/^0+/, '')
  return trimmedString || '0'
}
