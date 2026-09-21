package ingest

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func Decode(l types.Log) (store.Record, *big.Int, error) {
	var r store.Record
	if len(l.Topics) != 4 || l.Topics[0] != ContractABI.Events["TransactionEnqueued"].ID {
		return r, nil, fmt.Errorf("%w: invalid enqueue topics", ErrFatal)
	}
	for _, t := range l.Topics[1:3] {
		if !bytes.Equal(t[:12], make([]byte, 12)) {
			return r, nil, fmt.Errorf("%w: invalid address topic", ErrFatal)
		}
	}
	v, e := unpack("TransactionEnqueued", l.Data)
	if e != nil {
		return r, nil, e
	}
	chain := v[0].(*big.Int)
	gas := v[1].(*big.Int)
	timestamp := v[3].(*big.Int)
	idx := new(big.Int).SetBytes(l.Topics[3][:])
	if !gas.IsUint64() || !timestamp.IsUint64() || !idx.IsUint64() {
		return r, nil, fmt.Errorf("%w: enqueue integer overflow", ErrFatal)
	}
	r = store.Record{
		Index:       idx.Uint64(),
		Target:      common.BytesToAddress(l.Topics[2][:]),
		Origin:      common.BytesToAddress(l.Topics[1][:]),
		Data:        hexutil.Encode(v[2].([]byte)),
		GasLimit:    gas.String(),
		BlockNumber: l.BlockNumber,
		Timestamp:   timestamp.Uint64(),
		BlockHash:   l.BlockHash,
		TxHash:      l.TxHash,
		LogIndex:    l.Index,
	}
	return r, chain, nil
}
