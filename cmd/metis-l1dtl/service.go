package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/server"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func runService(ctx context.Context, cfg config.Config) error {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rpc, err := ethclient.DialContext(initCtx, cfg.RPC)
	if err != nil {
		return err
	}
	defer rpc.Close()
	if err := validateL1(initCtx, rpc, cfg); err != nil {
		return err
	}
	db, err := store.Open(cfg.DB, store.Identity{
		Version: 1,
		L1:      cfg.L1ChainID,
		L2:      cfg.L2ChainID,
		Start:   cfg.Start,
		Manager: cfg.AddressManager,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			slog.Error("close database", "err", err)
		}
	}()

	status := &ingest.Status{}
	api := &server.Server{Config: cfg, RPC: rpc, Store: db, Status: status}
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	syncer := &ingest.Syncer{Config: cfg, RPC: rpc, Store: db, Status: status}
	return serve(ctx, httpServer, listener, syncer)
}

func validateL1(ctx context.Context, rpc *ethclient.Client, cfg config.Config) error {
	id, err := rpc.ChainID(ctx)
	if err != nil {
		return err
	}
	if !id.IsUint64() || id.Uint64() != cfg.L1ChainID {
		return fmt.Errorf("L1 chain ID mismatch")
	}
	code, err := rpc.CodeAt(ctx, cfg.AddressManager, nil)
	if err != nil {
		return err
	}
	if len(code) == 0 {
		return fmt.Errorf("AddressManager has no code")
	}
	return nil
}

// serve waits for HTTP shutdown and ingestion to finish before its caller closes
// the shared database and RPC client.
func serve(ctx context.Context, httpServer *http.Server, listener net.Listener, syncer *ingest.Syncer) error {
	syncCtx, stop := context.WithCancel(ctx)
	defer stop()
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		syncer.Run(syncCtx)
	}()
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	slog.Info("service started", "listen", listener.Addr().String(), "l2_chain_id", syncer.Config.L2ChainID)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	stop()
	if err := shutdownHTTP(httpServer); serveErr == nil {
		serveErr = err
	}
	<-syncDone
	return serveErr
}

func shutdownHTTP(httpServer *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		_ = httpServer.Close()
		return err
	}
	return nil
}
