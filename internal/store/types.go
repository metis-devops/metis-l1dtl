package store

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
)

var ErrIntegrity = errors.New("storage integrity error")
var identityKey = []byte("meta/identity")
var stateKey = []byte("meta/state")

type Identity struct {
	Version       int
	L1, L2, Start uint64
	Manager       common.Address
}

type State struct {
	Height *uint64
	Hash   common.Hash
	CTC    common.Address
	Latest *uint64
}

type Enqueue struct {
	Index       uint64         `json:"index"`
	Target      common.Address `json:"target"`
	Data        string         `json:"data"`
	GasLimit    string         `json:"gasLimit"`
	Origin      common.Address `json:"origin"`
	BlockNumber uint64         `json:"blockNumber"`
	Timestamp   uint64         `json:"timestamp"`
	CTCIndex    *uint64        `json:"ctcIndex"`
}

type Record struct {
	Enqueue
	BlockHash common.Hash
	TxHash    common.Hash
	LogIndex  uint
}
