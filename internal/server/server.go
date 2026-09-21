package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type Server struct {
	Config config.Config
	RPC    interface {
		HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	}
	Store  *store.Store
	Status *ingest.Status
	Now    func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.readiness)
	mux.HandleFunc("GET /enqueue/latest/{chainId}", s.dataHandler(s.enqueue))
	mux.HandleFunc("GET /enqueue/index/{index}/{chainId}", s.dataHandler(s.enqueue))
	mux.HandleFunc("GET /eth/context/latest", s.dataHandler(s.l1Context))
	mux.HandleFunc("GET /eth/context/blocknumber/{number}", s.dataHandler(s.l1Context))
	mux.HandleFunc("GET /highest/l1", s.dataHandler(s.highestL1))
	mux.HandleFunc("GET /eth/syncing/{chainId}", s.dataHandler(s.syncing))
	mux.HandleFunc("GET /transaction/latest/{chainId}", s.dataHandler(s.transaction))
	mux.HandleFunc("GET /transaction/index/{index}/{chainId}", s.dataHandler(s.transaction))
	mux.HandleFunc("GET /block/latest/{chainId}", s.dataHandler(s.block))
	mux.HandleFunc("GET /block/index/{index}/{chainId}", s.dataHandler(s.block))
	return mux
}

type endpoint func(*http.Request, *uint64) (any, int, error)

// dataHandler applies the same integrity guard and parameter validation to all
// data routes. Health and readiness have their own status semantics.
func (s *Server) dataHandler(handle endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Status.Snapshot(); err != nil {
			reply(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		index, err := s.validateRequest(r)
		if err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		result, code, err := handle(r, index)
		if err != nil {
			reply(w, code, map[string]string{"error": err.Error()})
			return
		}
		reply(w, code, result)
	}
}

func (s *Server) validateRequest(r *http.Request) (*uint64, error) {
	if chain := r.PathValue("chainId"); chain != "" {
		chainID, err := uintParam(chain)
		if err != nil || chainID != s.Config.L2ChainID {
			return nil, fmt.Errorf("unknown or invalid chain ID")
		}
	}
	if backend, ok := r.URL.Query()["backend"]; ok && (len(backend) != 1 || backend[0] != "l1") {
		return nil, fmt.Errorf("only backend=l1 is supported")
	}
	if raw := r.PathValue("index"); raw != "" {
		index, err := uintParam(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid index")
		}
		return &index, nil
	}
	return nil, nil
}

func uintParam(value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid integer")
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

func reply(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
