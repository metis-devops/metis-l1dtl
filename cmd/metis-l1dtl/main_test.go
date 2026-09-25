package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
)

type mockEth struct{ headers []*types.Header }

func (m *mockEth) ChainId() *hexutil.Big { return (*hexutil.Big)(big.NewInt(1)) }
func (m *mockEth) GetCode(_ common.Address, block string) (hexutil.Bytes, error) {
	if block != "latest" {
		return nil, fmt.Errorf("historical state unavailable")
	}
	return hexutil.Bytes{1}, nil
}
func (m *mockEth) Call(map[string]any, string) (hexutil.Bytes, error) {
	return nil, fmt.Errorf("eth_call unavailable")
}
func (m *mockEth) GetLogs(q map[string]any) []types.Log {
	topics, _ := json.Marshal(q["topics"])
	if q["fromBlock"] != "0x0" || !strings.Contains(string(topics), crypto.Keccak256Hash([]byte("CanonicalTransactionChain")).Hex()) {
		return []types.Log{}
	}
	data, _ := ingest.ContractABI.Events["AddressSet"].Inputs.NonIndexed().Pack(common.HexToAddress("0x100"), common.Address{})
	return []types.Log{{Address: common.HexToAddress("0x200"), BlockNumber: 0, BlockHash: m.headers[0].Hash(), Topics: []common.Hash{ingest.ContractABI.Events["AddressSet"].ID, crypto.Keccak256Hash([]byte("CanonicalTransactionChain"))}, Data: data}}
}
func (m *mockEth) GetBlockByNumber(n string, full bool) (any, error) {
	var h *types.Header
	if n == "latest" {
		h = m.headers[2]
	}
	if h == nil {
		i, e := hexutil.DecodeUint64(n)
		if e != nil || i >= uint64(len(m.headers)) {
			return nil, fmt.Errorf("missing header")
		}
		h = m.headers[i]
	}
	if !full {
		return h, nil
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["transactions"] = []any{}
	out["uncles"] = []any{}
	return out, nil
}

func TestServiceProcess(t *testing.T) {
	if os.Getenv("L1DTL_TEST_PROCESS") == "1" {
		var args []string
		if e := json.Unmarshal([]byte(os.Getenv("L1DTL_TEST_ARGS")), &args); e != nil {
			os.Exit(2)
		}
		os.Args = append([]string{"metis-l1dtl"}, args...)
		main()
		os.Exit(0)
	}
	eth := &mockEth{}
	var parent common.Hash
	for n := range int64(3) {
		h := &types.Header{Number: big.NewInt(n), Difficulty: big.NewInt(0), ParentHash: parent, Time: uint64(100 + n), TxHash: types.EmptyTxsHash, UncleHash: types.EmptyUncleHash}
		eth.headers = append(eth.headers, h)
		parent = h.Hash()
	}
	r := rpc.NewServer()
	if e := r.RegisterName("eth", eth); e != nil {
		t.Fatal(e)
	}
	defer r.Stop()
	upstream := httptest.NewServer(r)
	defer upstream.Close()
	db := filepath.Join(t.TempDir(), "db")
	args := []string{"--l1-rpc=" + upstream.URL, "--l1-chain-id=1", "--l2-chain-id=1088", "--address-manager=0x0000000000000000000000000000000000000200", "--l1-start-height=1", "--confirmations=0", "--poll-interval=10ms", "--db=" + db, "--listen=127.0.0.1:0"}
	for _, enableBlob := range []bool{false, true} {
		runArgs := append([]string{}, args...)
		if enableBlob {
			runArgs = append(runArgs, "--l1-beacon=http://127.0.0.1:1", "--batch-inbox-address=0x0000000000000000000000000000000000000300", "--batch-inbox-l1-height=1", "--batch-inbox-sender=0x0000000000000000000000000000000000000400", "--batch-inbox-blob-sender=0x0000000000000000000000000000000000000500")
		}
		raw, _ := json.Marshal(runArgs)
		for range 2 {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServiceProcess$")
			cmd.Env = append(os.Environ(), "L1DTL_TEST_PROCESS=1", "L1DTL_TEST_ARGS="+string(raw))
			stderr, e := cmd.StderrPipe()
			if e != nil {
				cancel()
				t.Fatal(e)
			}
			if e = cmd.Start(); e != nil {
				cancel()
				t.Fatal(e)
			}
			address := make(chan string, 1)
			go func() {
				scan := bufio.NewScanner(stderr)
				for scan.Scan() {
					if scan.Err() != nil {
						return
					}
					var line struct {
						Msg    string `json:"msg"`
						Listen string `json:"listen"`
					}
					if json.Unmarshal(scan.Bytes(), &line) == nil && line.Msg == "service started" {
						address <- line.Listen
					}
				}
			}()
			var base string
			select {
			case addr := <-address:
				base = "http://" + addr
			case <-ctx.Done():
				_ = cmd.Wait()
				cancel()
				t.Fatal("process did not start")
			}
			client := &http.Client{Timeout: time.Second}
			ready := false
			for ctx.Err() == nil {
				resp, e := client.Get(base + "/readyz")
				if e == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode == 200 {
						ready = true
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				_ = cmd.Wait()
				cancel()
				t.Fatal("process did not become ready")
			}
			resp, e := client.Get(base + "/highest/l1")
			if e != nil {
				cancel()
				_ = cmd.Wait()
				t.Fatal(e)
			}
			var result struct {
				BlockNumber uint64 `json:"blockNumber"`
			}
			e = json.NewDecoder(resp.Body).Decode(&result)
			_ = resp.Body.Close()
			if e != nil || result.BlockNumber != 2 {
				cancel()
				_ = cmd.Wait()
				t.Fatalf("checkpoint %+v: %v", result, e)
			}
			if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
				cancel()
				_ = cmd.Wait()
				t.Fatal(e)
			}
			e = cmd.Wait()
			cancel()
			if e != nil {
				t.Fatalf("shutdown: %v", e)
			}
		}
	}
}
