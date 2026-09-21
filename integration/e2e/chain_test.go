//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
)

//go:embed testdata/*.json testdata/Fixtures.sol
var fixtures embed.FS

type contract struct {
	address common.Address
	abi     abi.ABI
}

type chain struct {
	t               *testing.T
	ctx             context.Context
	url             string
	rpc             *rpc.Client
	client          *ethclient.Client
	account         common.Address
	manager         common.Address
	managerContract contract
	ctc             contract
	start           uint64
}

func newChain(t *testing.T, historicalRegistration bool) *chain {
	t.Helper()
	a := &chain{t: t, url: startAnvil(t)}
	var err error
	a.rpc, err = rpc.Dial(a.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.rpc.Close)
	a.client = ethclient.NewClient(a.rpc)
	var cancel context.CancelFunc
	a.ctx, cancel = context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	eventually(t, "Anvil RPC", func() (bool, error) {
		ctx, cancel := context.WithTimeout(a.ctx, time.Second)
		defer cancel()
		id, err := a.client.ChainID(ctx)
		return err == nil && id.Uint64() == 31337, err
	})
	var accounts []common.Address
	a.call(&accounts, "eth_accounts")
	if len(accounts) == 0 {
		t.Fatal("Anvil has no unlocked test accounts")
	}
	a.account = accounts[0]
	a.managerContract, _ = a.deploy("AddressManagerFixture")
	a.manager = a.managerContract.address
	if historicalRegistration {
		nonce, err := a.client.PendingNonceAt(a.ctx, a.account)
		if err != nil {
			t.Fatal(err)
		}
		// Register the next deployment's address before deployment, so bootstrap
		// must search pre-start AddressSet history while still scanning index 0.
		expected := crypto.CreateAddress(a.account, nonce+1)
		a.register(expected)
		a.mine(6) // Force bootstrap to traverse empty history pages.
		a.ctc, a.start = a.deploy("CTCFixture")
		if a.ctc.address != expected {
			t.Fatalf("deployment address %s != %s", a.ctc.address, expected)
		}
	} else {
		a.ctc, a.start = a.deploy("CTCFixture")
		a.register(a.ctc.address)
	}
	return a
}

func (a *chain) call(result any, method string, args ...any) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	if err := a.rpc.CallContext(ctx, result, method, args...); err != nil {
		a.t.Fatalf("%s: %v", method, err)
	}
}

func (a *chain) deploy(name string) (contract, uint64) {
	a.t.Helper()
	c, hash := a.queueDeployment(name)
	receipt := a.receipt(hash)
	if receipt.ContractAddress != c.address {
		a.t.Fatalf("unexpected deployment address: %s", receipt.ContractAddress)
	}
	return c, receipt.BlockNumber.Uint64()
}

func (a *chain) queueDeployment(name string) (contract, common.Hash) {
	a.t.Helper()
	raw, err := fixtures.ReadFile("testdata/" + name + ".json")
	if err != nil {
		a.t.Fatal(err)
	}
	var artifact struct {
		ABI          json.RawMessage `json:"abi"`
		Bytecode     string          `json:"bytecode"`
		SourceSHA256 string          `json:"sourceSHA256"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		a.t.Fatal(err)
	}
	source, err := fixtures.ReadFile("testdata/Fixtures.sol")
	if err != nil {
		a.t.Fatal(err)
	}
	if artifact.SourceSHA256 != fmt.Sprintf("%x", sha256.Sum256(source)) {
		a.t.Fatal("stale fixture artifact; run make e2e-fixtures")
	}
	parsed, err := abi.JSON(strings.NewReader(string(artifact.ABI)))
	if err != nil {
		a.t.Fatal(err)
	}
	// Compare topic placement as well as event signatures with the service ABI.
	for name, event := range parsed.Events {
		want, ok := ingest.ContractABI.Events[name]
		if !ok || want.ID != event.ID || len(want.Inputs) != len(event.Inputs) {
			a.t.Fatalf("fixture event mismatch: %s", name)
		}
		for i, arg := range event.Inputs {
			if arg.Indexed != want.Inputs[i].Indexed || arg.Type.String() != want.Inputs[i].Type.String() {
				a.t.Fatalf("fixture event input mismatch: %s[%d]", name, i)
			}
		}
	}
	code, err := hexutil.Decode(artifact.Bytecode)
	if err != nil || len(code) == 0 {
		a.t.Fatalf("invalid deployment bytecode: %v", err)
	}
	nonce, err := a.client.PendingNonceAt(a.ctx, a.account)
	if err != nil {
		a.t.Fatal(err)
	}
	return contract{address: crypto.CreateAddress(a.account, nonce), abi: parsed}, a.send(nil, code)
}

func (a *chain) send(to *common.Address, data []byte) common.Hash {
	a.t.Helper()
	nonce, err := a.client.PendingNonceAt(a.ctx, a.account)
	if err != nil {
		a.t.Fatal(err)
	}
	args := map[string]any{"from": a.account, "data": hexutil.Bytes(data), "gas": "0x1e8480", "nonce": hexutil.Uint64(nonce)}
	if to != nil {
		args["to"] = *to
	}
	var hash common.Hash
	a.call(&hash, "eth_sendTransaction", args)
	return hash
}

func (a *chain) transact(c contract, method string, args ...any) common.Hash {
	a.t.Helper()
	data, err := c.abi.Pack(method, args...)
	if err != nil {
		a.t.Fatal(err)
	}
	return a.send(&c.address, data)
}

func (a *chain) receipt(hash common.Hash) *types.Receipt {
	a.t.Helper()
	var receipt *types.Receipt
	eventually(a.t, "transaction "+hash.Hex(), func() (bool, error) {
		ctx, cancel := context.WithTimeout(a.ctx, time.Second)
		defer cancel()
		var err error
		receipt, err = a.client.TransactionReceipt(ctx, hash)
		return receipt != nil, err
	})
	if receipt.Status != types.ReceiptStatusSuccessful {
		a.t.Fatalf("transaction reverted: %s", hash)
	}
	return receipt
}

func (a *chain) register(address common.Address) *types.Receipt {
	a.t.Helper()
	return a.receipt(a.transact(a.managerContract, "setAddress", "CanonicalTransactionChain", address))
}

var depositTarget = common.HexToAddress("0x1234567890123456789012345678901234567890")

const depositGas = uint64(250000)

func (a *chain) enqueue(c contract, chainID uint64, data []byte) *types.Receipt {
	a.t.Helper()
	return a.receipt(a.transact(c, "enqueue", new(big.Int).SetUint64(chainID), depositTarget, new(big.Int).SetUint64(depositGas), data))
}

func (a *chain) enqueueAt(c contract, index uint64, data []byte) common.Hash {
	a.t.Helper()
	return a.transact(c, "enqueueAt", big.NewInt(1088), depositTarget, new(big.Int).SetUint64(depositGas), data, new(big.Int).SetUint64(index))
}

func (a *chain) mine(n uint64)         { a.t.Helper(); a.call(nil, "anvil_mine", hexutil.EncodeUint64(n)) }
func (a *chain) automine(enabled bool) { a.t.Helper(); a.call(nil, "evm_setAutomine", enabled) }
func (a *chain) tip() uint64 {
	a.t.Helper()
	n, err := a.client.BlockNumber(a.ctx)
	if err != nil {
		a.t.Fatal(err)
	}
	return n
}

func (a *chain) header(n uint64) *types.Header {
	a.t.Helper()
	h, err := a.client.HeaderByNumber(a.ctx, new(big.Int).SetUint64(n))
	if err != nil {
		a.t.Fatal(err)
	}
	return h
}

func (a *chain) sameBlock(hashes ...common.Hash) []*types.Receipt {
	a.t.Helper()
	a.mine(1)
	a.automine(true)
	receipts := make([]*types.Receipt, len(hashes))
	for i, hash := range hashes {
		receipts[i] = a.receipt(hash)
		if i > 0 && (receipts[i].BlockHash != receipts[0].BlockHash || receipts[i].TransactionIndex != receipts[i-1].TransactionIndex+1) {
			a.t.Fatal("transactions were not mined in the requested order in one block")
		}
	}
	return receipts
}
