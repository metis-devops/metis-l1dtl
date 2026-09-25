import { ethers } from 'ethersv6'

export enum QueueOrigin {
  Sequencer = 'sequencer',
  L1ToL2 = 'l1',
}

/**
 * Transaction & Blocks. These are the true data-types we expect
 * from running a batch submitter.
 */
export interface L2Transaction extends ethers.TransactionResponse {
  l1BlockNumber: number
  l1Timestamp: number
  index: number
  queueIndex: number
  l1TxOrigin: string
  queueOrigin: string
  rawTransaction: string
  seqR: string | undefined | null
  seqS: string | undefined | null
  seqV: string | undefined | null
}

