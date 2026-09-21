package config

import (
	"flag"
	"fmt"
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
}

func Parse(args []string) (Config, error) {
	var c Config
	var manager string
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
	if err := f.Parse(args); err != nil {
		return c, err
	}
	startSet := false
	f.Visit(func(v *flag.Flag) {
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
	return c, nil
}
