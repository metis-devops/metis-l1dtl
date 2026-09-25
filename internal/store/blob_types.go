package store

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
)

const BlobRetentionSeconds uint64 = 7 * 24 * 60 * 60

// BlobIdentity extends, without replacing, the deposit database identity.
type BlobIdentity struct {
	Version                        int
	Inbox, BatchSender, BlobSender common.Address
	Start                          uint64
}

type BlobState struct {
	Revision        uint64
	Height          *uint64
	Hash            common.Hash
	Cutoff          uint64
	RetentionHeight *uint64
	RetentionHash   common.Hash
	Latest          *uint64
	Auth            json.RawMessage
}

type Frame struct {
	ID     [16]byte
	Number uint16
	Last   bool
	Data   []byte
}

type BlobTransaction struct {
	Hash, BlockHash   common.Hash
	Height, Timestamp uint64
	TxIndex           uint
	Missing           bool
	Frames            []Frame
}

type Signature struct {
	V uint64 `json:"v"`
	R string `json:"r"`
	S string `json:"s"`
}

type DecodedTransaction struct {
	Nonce    string          `json:"nonce"`
	GasPrice string          `json:"gasPrice"`
	GasLimit string          `json:"gasLimit"`
	Value    string          `json:"value"`
	Target   *common.Address `json:"target"`
	Data     string          `json:"data"`
	Sig      Signature       `json:"sig"`
}

type BlockTransaction struct {
	Index       uint64              `json:"index"`
	BatchIndex  uint64              `json:"batchIndex"`
	BlockNumber uint64              `json:"blockNumber"`
	Timestamp   uint64              `json:"timestamp"`
	GasLimit    string              `json:"gasLimit"`
	Target      common.Address      `json:"target"`
	Origin      common.Address      `json:"origin"`
	Data        string              `json:"data"`
	QueueOrigin string              `json:"queueOrigin"`
	Value       string              `json:"value"`
	QueueIndex  *uint64             `json:"queueIndex"`
	Decoded     *DecodedTransaction `json:"decoded"`
	Confirmed   bool                `json:"confirmed"`
	SeqSign     *string             `json:"seqSign"`
}

type Block struct {
	Index        uint64             `json:"index"`
	BatchIndex   uint64             `json:"batchIndex"`
	Timestamp    uint64             `json:"timestamp"`
	ExtraData    string             `json:"extraData"`
	Transactions []BlockTransaction `json:"transactions"`
	Confirmed    bool               `json:"confirmed"`
}

type BlockBatch struct {
	Index             uint64         `json:"index"`
	Root              common.Hash    `json:"root"`
	Size              uint64         `json:"size"`
	PrevTotalElements uint64         `json:"prevTotalElements"`
	ExtraData         string         `json:"extraData"`
	BlockNumber       uint64         `json:"blockNumber"`
	Timestamp         uint64         `json:"timestamp"`
	Submitter         common.Address `json:"submitter"`
	L1TransactionHash common.Hash    `json:"l1TransactionHash"`
}

type BlockResponse struct {
	Block *Block      `json:"block"`
	Batch *BlockBatch `json:"batch"`
}

// ChannelRecord tracks all source transactions, so expiration removes every
// derived block as soon as even one required source leaves the window.
type ChannelRecord struct {
	ID        string
	Sources   []common.Hash
	Blocks    []uint64
	Batch     BlockBatch
	ExpiresAt uint64 // minimum source timestamp (compared with the cutoff)
}

type BlobChannel struct {
	Record ChannelRecord
	Blocks []Block
}
