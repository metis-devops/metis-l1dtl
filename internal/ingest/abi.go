package ingest

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

//go:embed contract.abi.json
var contractABIJSON string

var ContractABI = mustABI(contractABIJSON)
var nameHash = crypto.Keccak256Hash([]byte("CanonicalTransactionChain"))

func mustABI(s string) abi.ABI {
	a, e := abi.JSON(strings.NewReader(s))
	if e != nil {
		panic(e)
	}
	return a
}

func equalTopics(a, b []common.Hash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateLog(l types.Log, from, end uint64) error {
	if l.Removed || l.BlockNumber < from || l.BlockNumber > end || len(l.Topics) == 0 {
		return fmt.Errorf("%w: invalid log range or metadata", ErrFatal)
	}
	return nil
}

func unpack(name string, data []byte) ([]any, error) {
	args := ContractABI.Events[name].Inputs.NonIndexed()
	v, e := args.Unpack(data)
	if e != nil {
		return nil, fmt.Errorf("%w: %w", ErrFatal, e)
	}
	raw, e := args.Pack(v...)
	if e != nil || !bytes.Equal(raw, data) {
		return nil, fmt.Errorf("%w: noncanonical event data", ErrFatal)
	}
	return v, nil
}
