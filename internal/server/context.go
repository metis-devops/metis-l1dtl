package server

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"time"
)

// l1Context preserves the legacy timestamp behavior: latest uses wall-clock
// time, while a numbered context uses the actual block timestamp.
func (s *Server) l1Context(r *http.Request, _ *uint64) (any, int, error) {
	var requested *uint64
	if raw := r.PathValue("number"); raw != "" {
		blockNumber, err := uintParam(raw)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		requested = &blockNumber
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tip, err := s.RPC.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsUint64() {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("invalid L1 tip")
	}
	blockNumber := uint64(0)
	if tip.Number.Uint64() >= s.Config.Confirmations {
		blockNumber = tip.Number.Uint64() - s.Config.Confirmations
	}
	if requested != nil {
		if *requested > blockNumber {
			return map[string]any{"blockNumber": nil, "blockHash": nil, "timestamp": nil}, http.StatusOK, nil
		}
		blockNumber = *requested
	}
	header, err := s.RPC.HeaderByNumber(ctx, new(big.Int).SetUint64(blockNumber))
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != blockNumber {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("invalid L1 header")
	}
	timestamp := header.Time
	if requested == nil {
		now := time.Now
		if s.Now != nil {
			now = s.Now
		}
		timestamp = uint64(now().Unix())
	}
	return map[string]any{"blockNumber": blockNumber, "blockHash": header.Hash(), "timestamp": timestamp}, http.StatusOK, nil
}
