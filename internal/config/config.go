package config

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type Config struct {
	RPC                             string
	L1ChainID, L2ChainID            uint64
	AddressManager                  common.Address
	Start, Confirmations, BatchSize uint64
	Poll                            time.Duration
	DB, Listen                      string
	Beacon                          string
	Inbox, InboxSender, BlobSender  common.Address
	InboxStart                      uint64
}

func (c Config) BlobEnabled() bool { return c.Beacon != "" }

func Parse(args []string) (Config, error) {
	var c Config
	var manager string
	var inbox, sender, blobSender string
	f := flag.NewFlagSet("metis-l1dtl", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	f.StringVar(&c.RPC, "l1-rpc", "", "L1 execution RPC URL")
	f.Uint64Var(&c.L1ChainID, "l1-chain-id", 0, "expected L1 chain ID")
	f.Uint64Var(&c.L2ChainID, "l2-chain-id", 0, "L2 chain ID")
	f.StringVar(&manager, "address-manager", "", "AddressManager address")
	f.Uint64Var(&c.Start, "l1-start-height", 0, "CTC deployment block (inclusive)")
	f.Uint64Var(&c.Confirmations, "confirmations", 35, "confirmation depth")
	f.Uint64Var(&c.BatchSize, "batch-size", 2000, "blocks per scan")
	f.DurationVar(&c.Poll, "poll-interval", 5*time.Second, "poll interval")
	f.StringVar(&c.DB, "db", "data", "Pebble database directory")
	f.StringVar(&c.Listen, "listen", "0.0.0.0:7878", "HTTP address")
	f.StringVar(&c.Beacon, "l1-beacon", "", "Beacon REST URL (enables optional Blob ingestion)")
	f.StringVar(&inbox, "batch-inbox-address", "", "batch Inbox address")
	f.Uint64Var(&c.InboxStart, "batch-inbox-l1-height", 0, "inclusive Inbox scan start")
	f.StringVar(&sender, "batch-inbox-sender", "", "initial Batch sender before applicable events")
	f.StringVar(&blobSender, "batch-inbox-blob-sender", "", "initial Blob sender before applicable events")
	if err := f.Parse(args); err != nil {
		return c, err
	}
	startSet := false
	blobFlags := 0
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "l1-beacon", "batch-inbox-address", "batch-inbox-l1-height", "batch-inbox-sender", "batch-inbox-blob-sender":
			blobFlags++
		}
		if v.Name == "l1-start-height" {
			startSet = true
		}
	})
	if f.NArg() != 0 || c.RPC == "" || c.L1ChainID == 0 || c.L2ChainID == 0 || !common.IsHexAddress(manager) || !startSet || c.BatchSize == 0 || c.Poll <= 0 || c.DB == "" || c.Listen == "" {
		return c, fmt.Errorf("require --l1-rpc, --l1-chain-id, --l2-chain-id, --address-manager, --l1-start-height; scan batch size and poll interval must be positive")
	}
	c.AddressManager = common.HexToAddress(manager)
	if c.AddressManager == (common.Address{}) {
		return c, fmt.Errorf("zero AddressManager")
	}
	if blobFlags != 0 {
		u, err := url.Parse(c.Beacon)
		if blobFlags != 5 || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
			return c, fmt.Errorf("blob ingestion requires all five --l1-beacon/--batch-inbox-* flags and an HTTP(S) Beacon URL")
		}
		for _, a := range []string{inbox, sender, blobSender} {
			if !common.IsHexAddress(a) || common.HexToAddress(a) == (common.Address{}) {
				return c, fmt.Errorf("blob addresses must be nonzero addresses")
			}
		}
		c.Inbox, c.InboxSender, c.BlobSender = common.HexToAddress(inbox), common.HexToAddress(sender), common.HexToAddress(blobSender)
	}
	return c, nil
}
