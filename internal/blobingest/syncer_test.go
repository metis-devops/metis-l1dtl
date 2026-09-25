package blobingest

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/metis-devops/metis-l1dtl/internal/blob"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type rpcFixture struct {
	blocks      []*types.Block
	logs        []types.Log
	receipts    map[common.Hash]*types.Receipt
	logErr      error
	receiptErr  error
	logQueries  []ethereum.FilterQuery
	headerCalls int
}

func (f *rpcFixture) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	f.headerCalls++
	if n == nil {
		return f.blocks[len(f.blocks)-1].Header(), nil
	}
	if !n.IsUint64() || n.Uint64() >= uint64(len(f.blocks)) {
		return nil, ethereum.NotFound
	}
	return f.blocks[n.Uint64()].Header(), nil
}
func (f *rpcFixture) BlockByNumber(_ context.Context, n *big.Int) (*types.Block, error) {
	if n.Uint64() >= uint64(len(f.blocks)) {
		return nil, ethereum.NotFound
	}
	return f.blocks[n.Uint64()], nil
}
func (f *rpcFixture) BlockByHash(_ context.Context, hash common.Hash) (*types.Block, error) {
	for _, b := range f.blocks {
		if b.Hash() == hash {
			return b, nil
		}
	}
	return nil, ethereum.NotFound
}
func (f *rpcFixture) TransactionReceipt(_ context.Context, hash common.Hash) (*types.Receipt, error) {
	if f.receiptErr != nil {
		return nil, f.receiptErr
	}
	r := f.receipts[hash]
	if r == nil {
		return nil, ethereum.NotFound
	}
	return r, nil
}
func (f *rpcFixture) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.logQueries = append(f.logQueries, q)
	if f.logErr != nil {
		return nil, f.logErr
	}
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < q.FromBlock.Uint64() || l.BlockNumber > q.ToBlock.Uint64() {
			continue
		}
		addressOK := false
		for _, a := range q.Addresses {
			if a == l.Address {
				addressOK = true
			}
		}
		if !addressOK {
			continue
		}
		matches := true
		for i, choices := range q.Topics {
			if len(choices) == 0 {
				continue
			}
			ok := false
			for _, c := range choices {
				if i < len(l.Topics) && l.Topics[i] == c {
					ok = true
				}
			}
			if !ok {
				matches = false
			}
		}
		if matches {
			out = append(out, l)
		}
	}
	return out, nil
}

type sourceFixture struct {
	frames []store.Frame
	err    error
	calls  int
}

func (f *sourceFixture) Frames(context.Context, *types.Header, []common.Hash) ([]store.Frame, error) {
	f.calls++
	return f.frames, f.err
}
func changeLog(height uint64, txIndex, index uint, manager, next, old common.Address) types.Log {
	event := ingest.ContractABI.Events["AddressSet"]
	data, _ := event.Inputs.NonIndexed().Pack(next, old)
	return types.Log{Address: manager, BlockNumber: height, TxIndex: txIndex, Index: index, Topics: []common.Hash{event.ID, senderManagerName}, Data: data}
}
func senderLog(height, effective uint64, txIndex, index uint, manager, sender common.Address, kind uint64) types.Log {
	return types.Log{Address: manager, BlockNumber: height, TxIndex: txIndex, Index: index, Topics: []common.Hash{senderTopic, common.BigToHash(number(effective)), common.BytesToHash(sender[:]), common.BigToHash(number(kind))}}
}
func signed(t *testing.T, tx *types.Transaction, keyByte string) *types.Transaction {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat(keyByte, 32))
	if err != nil {
		t.Fatal(err)
	}
	out, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(1)), key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func wallet(t *testing.T, keyByte string) common.Address {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat(keyByte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return crypto.PubkeyToAddress(key.PublicKey)
}
func fixtureSyncer(t *testing.T) (*Syncer, *rpcFixture, *sourceFixture, common.Hash) {
	t.Helper()
	manager, inbox := common.HexToAddress("0x100"), common.HexToAddress("0x200")
	rpc := &rpcFixture{receipts: map[common.Hash]*types.Receipt{}}
	blobTx := signed(t, types.NewTx(&types.BlobTx{ChainID: uint256.NewInt(1), Nonce: 0, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Gas: 50000, To: inbox, Value: uint256.NewInt(0), BlobFeeCap: uint256.NewInt(1), BlobHashes: []common.Hash{common.HexToHash("0x01")}}), "01")
	data := make([]byte, 102)
	data[0] = 3
	data[33] = 7
	binary.BigEndian.PutUint64(data[58:66], 1000)
	binary.BigEndian.PutUint32(data[66:70], 2)
	copy(data[70:], blobTx.Hash().Bytes())
	commitment := signed(t, types.NewTx(&types.LegacyTx{Nonce: 1, GasPrice: big.NewInt(1), Gas: 50000, To: &inbox, Value: big.NewInt(0), Data: data}), "01")
	for n := range uint64(5) {
		var txs []*types.Transaction
		if n == 1 {
			txs = append(txs, blobTx)
		}
		if n == 3 {
			txs = append(txs, commitment)
		}
		h := &types.Header{Number: number(n), Difficulty: big.NewInt(0), Time: 1_700_000_000 + n}
		if n > 0 {
			h.ParentHash = rpc.blocks[n-1].Hash()
		}
		slot := n + 100
		h.SlotNumber = &slot
		b := types.NewBlockWithHeader(h).WithBody(types.Body{Transactions: txs})
		rpc.blocks = append(rpc.blocks, b)
		for i, tx := range txs {
			rpc.receipts[tx.Hash()] = &types.Receipt{TxHash: tx.Hash(), BlockHash: b.Hash(), BlockNumber: b.Number(), TransactionIndex: uint(i), Status: 1}
		}
	}
	rpc.logs = []types.Log{changeLog(0, 0, 0, manager, common.HexToAddress("0x300"), common.Address{})}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{L1ChainID: 1, L2ChainID: 1088, AddressManager: manager, Inbox: inbox, InboxSender: wallet(t, "01"), BlobSender: wallet(t, "01"), InboxStart: 1, BatchSize: 2, Poll: time.Millisecond, Beacon: "http://beacon.test"}
	if err := db.InitBlob(store.BlobIdentity{Version: 1, Inbox: inbox, BatchSender: cfg.InboxSender, BlobSender: cfg.BlobSender, Start: cfg.InboxStart}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../blob/testdata/typescript-blob.hex")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hexutil.Decode(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := blob.DecodeBlob(encoded)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := blob.ParseFrames(payload)
	if err != nil {
		t.Fatal(err)
	}
	src := &sourceFixture{frames: frames}
	status := &ingest.Status{}
	status.EnableBlob()
	status.Set(true, nil)
	return &Syncer{Config: cfg, RPC: rpc, Beacon: src, Store: db, Status: status}, rpc, src, blobTx.Hash()
}
func advance(t *testing.T, s *Syncer) {
	t.Helper()
	for range 20 {
		done, err := s.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("did not catch up")
}
func TestSyncBlobEndpointsRestartAndExpiration(t *testing.T) {
	s, rpc, src, hash := fixtureSyncer(t)
	if err := s.Store.Commit(store.State{}, store.State{}, []store.Record{{Index: 0}, {Index: 1}}); err != nil {
		t.Fatal(err)
	}
	advance(t, s)
	out, err := s.Store.GetBlock(nil)
	if err != nil || out.Block == nil || out.Block.Index != 1000 || out.Batch.Index != 7 {
		t.Fatal(out, err)
	}
	if ready, err := s.Status.Snapshot(); !ready || err != nil {
		t.Fatal(ready, err)
	}
	if src.calls != 1 {
		t.Fatal("unexpected Beacon calls", src.calls)
	}
	restarted := &Syncer{Config: s.Config, RPC: rpc, Beacon: src, Store: s.Store, Status: s.Status}
	advance(t, restarted)
	if src.calls != 1 {
		t.Fatal("refetched persisted Blob")
	}
	h := &types.Header{Number: number(5), Difficulty: big.NewInt(0), ParentHash: rpc.blocks[4].Hash(), Time: rpc.blocks[1].Time() + store.BlobRetentionSeconds + 1}
	rpc.blocks = append(rpc.blocks, types.NewBlockWithHeader(h))
	advance(t, restarted)
	if out, err := s.Store.GetBlock(nil); err != nil || out.Block != nil {
		t.Fatal("expired channel survived", out, err)
	}
	if tx, err := s.Store.BlobTransaction(hash); err != nil || tx != nil {
		t.Fatal("expired source survived", err)
	}
}
func TestBlobMissingVersusRetry(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "missing"}[missing], func(t *testing.T) {
			s, _, src, hash := fixtureSyncer(t)
			if missing {
				src.err = blob.ErrMissing
			} else {
				src.err = errors.New("temporary")
			}
			if _, err := s.Step(context.Background()); err != nil {
				t.Fatal(err)
			} // bootstrap and blocks 1..2
			before, _ := s.Store.BlobState()
			done, err := s.Step(context.Background())
			after, _ := s.Store.BlobState()
			if missing {
				if err != nil || !done {
					t.Fatal(done, err)
				}
				tx, err := s.Store.BlobTransaction(hash)
				if err != nil || tx == nil || !tx.Missing {
					t.Fatal(tx, err)
				}
				src.err = nil
				advance(t, s)
				if src.calls != 1 {
					t.Fatal("retried missing data")
				}
			} else {
				if err == nil || done || after.Revision != before.Revision || *after.Height != *before.Height {
					t.Fatal("failed scan advanced", err)
				}
				if ready, _ := s.Status.Snapshot(); ready {
					t.Fatal("ready during retry")
				}
				// Deposit commits remain available while Blob retries.
				height := uint64(10)
				if err := s.Store.Commit(store.State{}, store.State{Height: &height}, nil); err != nil {
					t.Fatal(err)
				}
				src.err = nil
				advance(t, s)
			}
		})
	}
}
func TestOldHistoryDoesNotFetchBeaconAndCheckpointReorg(t *testing.T) {
	s, rpc, src, _ := fixtureSyncer(t)
	h := &types.Header{Number: number(5), Difficulty: big.NewInt(0), ParentHash: rpc.blocks[4].Hash(), Time: rpc.blocks[1].Time() + store.BlobRetentionSeconds + 1}
	rpc.blocks = append(rpc.blocks, types.NewBlockWithHeader(h))
	advance(t, s)
	if src.calls != 0 {
		t.Fatal("fetched expired Blob")
	}
	header := rpc.blocks[5].Header()
	header.Extra = []byte{1}
	rpc.blocks[5] = types.NewBlockWithHeader(header)
	if _, err := s.Step(context.Background()); !errors.Is(err, ingest.ErrFatal) {
		t.Fatal("reorg accepted", err)
	}
}
func TestBootstrapFailureDoesNotPublishProgress(t *testing.T) {
	s, rpc, _, _ := fixtureSyncer(t)
	rpc.logErr = errors.New("temporary")
	if _, err := s.Step(context.Background()); err == nil {
		t.Fatal("missing error")
	}
	if s.bootstrap.next != 0 {
		t.Fatal("advanced failed bootstrap page")
	}
	st, _ := s.Store.BlobState()
	if st.Height != nil {
		t.Fatal("published bootstrap checkpoint")
	}
	rpc.logErr = nil
	advance(t, s)
}
func TestAuthorizationHistoryAndEffectiveHeight(t *testing.T) {
	s, rpc, _, _ := fixtureSyncer(t)
	m1, m2 := common.HexToAddress("0x300"), common.HexToAddress("0x400")
	senderA, senderB := wallet(t, "01"), wallet(t, "02")
	a := newAuthorization()
	a.ensure(m1)
	a.ensure(m2)
	logs := []types.Log{changeLog(0, 0, 0, s.Config.AddressManager, m1, common.Address{}), senderLog(0, 5, 0, 1, m1, senderB, 0), senderLog(1, 0, 0, 2, m1, senderB, 1)}
	for _, l := range logs {
		if err := a.apply(l, s.Config.AddressManager); err != nil {
			t.Fatal(err)
		}
	}
	if a.sender(4, false, senderA) != senderA || a.sender(5, false, senderA) != senderB || a.sender(1, true, senderA) != senderB {
		t.Fatal("effective height/type mismatch")
	}
	if err := a.apply(senderLog(2, 5, 0, 3, m1, senderA, 0), s.Config.AddressManager); err != nil {
		t.Fatal(err)
	}
	if a.sender(5, false, senderB) != senderA {
		t.Fatal("same-key update ignored")
	}
	if err := a.apply(changeLog(3, 1, 4, s.Config.AddressManager, m2, m1), s.Config.AddressManager); err != nil {
		t.Fatal(err)
	}
	if a.sender(5, false, senderB) != senderB {
		t.Fatal("leaked old manager schedule")
	}
	// New manager had an event before registration; discover it via backfill.
	rpc.logs = append(rpc.logs, senderLog(0, 1, 0, 1, m2, senderB, 0), changeLog(2, 0, 0, s.Config.AddressManager, m2, m1))
	a = newAuthorization()
	a.Manager = m1
	a.ensure(m1)
	updates, err := s.authLogs(context.Background(), a, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range updates {
		if err := a.apply(l, s.Config.AddressManager); err != nil {
			t.Fatal(err)
		}
	}
	if a.sender(2, false, senderA) != senderB {
		t.Fatal("new manager history not backfilled")
	}
}
func TestLogValidationAndDeduplication(t *testing.T) {
	s, _, _, _ := fixtureSyncer(t)
	m := common.HexToAddress("0x300")
	l := senderLog(1, 2, 0, 0, m, s.Config.InboxSender, 0)
	logs, err := orderedLogs([]types.Log{l, l})
	if err != nil || len(logs) != 1 {
		t.Fatal(logs, err)
	}
	other := l
	other.Topics = append([]common.Hash{}, l.Topics...)
	other.Topics[2] = common.HexToHash("0x01")
	if _, err := orderedLogs([]types.Log{l, other}); !errors.Is(err, ingest.ErrFatal) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*types.Log){func(l *types.Log) { l.Removed = true }, func(l *types.Log) { l.BlockNumber = 4 }} {
		bad := l
		mutate(&bad)
		if err := validateLog(bad, 0, 2); !errors.Is(err, ingest.ErrFatal) {
			t.Fatal(err)
		}
	}
	a := newAuthorization()
	a.ensure(m)
	bad := l
	bad.Topics = append([]common.Hash{}, l.Topics...)
	bad.Topics[3] = common.HexToHash("0x02")
	if err := a.apply(bad, s.Config.AddressManager); !errors.Is(err, ingest.ErrFatal) {
		t.Fatal(err)
	}
}

func TestSameBlockSenderChangeAppliesAfterItsTransaction(t *testing.T) {
	s, rpc, _, _ := fixtureSyncer(t)
	old := rpc.blocks[3].Transactions()[0]
	next := signed(t, types.NewTx(&types.LegacyTx{Nonce: 3, GasPrice: big.NewInt(1), Gas: 50000, To: old.To(), Value: big.NewInt(0), Data: old.Data()}), "02")
	b := rpc.blocks[3].WithBody(types.Body{Transactions: []*types.Transaction{old, next}})
	rpc.blocks[3] = b
	rpc.receipts[next.Hash()] = &types.Receipt{TxHash: next.Hash(), BlockHash: b.Hash(), BlockNumber: b.Number(), TransactionIndex: 1, Status: 1}
	m := common.HexToAddress("0x300")
	a := newAuthorization()
	a.Manager = m
	a.ensure(m)
	logs := []types.Log{senderLog(3, 3, 0, 0, m, wallet(t, "02"), 0)}
	_, channels, err := s.scan(context.Background(), a, logs, 3, 3, 0)
	if err != nil || len(channels) != 2 {
		t.Fatalf("same-block sender ordering: channels=%d err=%v", len(channels), err)
	}
}
func TestIndependentPruningDuringRetryAndRetentionAnchor(t *testing.T) {
	s, rpc, src, _ := fixtureSyncer(t)
	advance(t, s)
	before, _ := s.Store.BlobState()
	old := rpc.blocks[1].Transactions()[0]
	fresh := signed(t, types.NewTx(&types.BlobTx{ChainID: uint256.NewInt(1), Nonce: 8, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Gas: 50000, To: s.Config.Inbox, Value: uint256.NewInt(0), BlobFeeCap: uint256.NewInt(1), BlobHashes: old.BlobHashes()}), "01")
	data := append([]byte{}, rpc.blocks[3].Transactions()[0].Data()...)
	copy(data[70:], fresh.Hash().Bytes())
	submit := signed(t, types.NewTx(&types.LegacyTx{Nonce: 9, GasPrice: big.NewInt(1), Gas: 50000, To: &s.Config.Inbox, Value: big.NewInt(0), Data: data}), "01")
	timestamp := rpc.blocks[1].Time() + store.BlobRetentionSeconds + 1
	for n := uint64(5); n <= 6; n++ {
		tx := fresh
		if n == 6 {
			tx = submit
		}
		b := types.NewBlockWithHeader(&types.Header{Number: number(n), Difficulty: big.NewInt(0), ParentHash: rpc.blocks[n-1].Hash(), Time: timestamp + n}).WithBody(types.Body{Transactions: []*types.Transaction{tx}})
		rpc.blocks = append(rpc.blocks, b)
		rpc.receipts[tx.Hash()] = &types.Receipt{TxHash: tx.Hash(), BlockHash: b.Hash(), BlockNumber: b.Number(), TransactionIndex: 0, Status: 1}
	}
	src.err = errors.New("Beacon temporarily unavailable")
	if _, err := s.Step(context.Background()); err == nil {
		t.Fatal("expected retry")
	}
	after, _ := s.Store.BlobState()
	if *after.Height != *before.Height || after.Cutoff <= before.Cutoff {
		t.Fatal("cleanup moved checkpoint or failed")
	}
	if out, err := s.Store.GetBlock(nil); err != nil || out.Block != nil {
		t.Fatal("expired data visible during retry", err)
	}
	// Retention is irreversible and therefore also has a persisted fork anchor.
	h := rpc.blocks[6].Header()
	h.Extra = []byte{99}
	rpc.blocks[6] = types.NewBlockWithHeader(h).WithBody(*rpc.blocks[6].Body())
	if _, err := s.Step(context.Background()); !errors.Is(err, ingest.ErrFatal) {
		t.Fatal("changed retention anchor accepted", err)
	}
}
func TestFatalBlobEvidencePublishesGlobalHalt(t *testing.T) {
	s, _, src, _ := fixtureSyncer(t)
	src.err = blob.ErrInvalid
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.Run(ctx)
	if ready, err := s.Status.Snapshot(); ready || !errors.Is(err, blob.ErrInvalid) {
		t.Fatal("fatal not published", ready, err)
	}
	if err := s.Status.CommitIfHealthy(func() error { t.Fatal("commit after Blob halt"); return nil }); err == nil {
		t.Fatal("fatal barrier absent")
	}
}

func TestSenderBackfillPagesRetryWithoutPartialPublication(t *testing.T) {
	s, rpc, _, _ := fixtureSyncer(t)
	s.Config.BatchSize = 1
	manager := common.HexToAddress("0x400")
	sender := wallet(t, "02")
	rpc.logs = append(rpc.logs, senderLog(0, 1, 0, 1, manager, sender, 0))
	a := newAuthorization()
	if err := s.backfillManager(context.Background(), a, manager, 3); !errors.Is(err, errBackfillPending) {
		t.Fatal(err)
	}
	if a.Known[manager] != nil || s.backfills[manager].next != 1 {
		t.Fatal("published incomplete history")
	}
	rpc.logErr = errors.New("transient")
	if err := s.backfillManager(context.Background(), a, manager, 3); err == nil {
		t.Fatal("missing retry")
	}
	if s.backfills[manager].next != 1 {
		t.Fatal("failed page advanced")
	}
	rpc.logErr = nil
	if err := s.backfillManager(context.Background(), a, manager, 3); !errors.Is(err, errBackfillPending) {
		t.Fatal(err)
	}
	if err := s.backfillManager(context.Background(), a, manager, 3); err != nil {
		t.Fatal(err)
	}
	a.Manager = manager
	if a.sender(3, false, s.Config.InboxSender) != sender {
		t.Fatal("lost paged history")
	}
	h := rpc.blocks[2].Header()
	h.Extra = []byte{9}
	rpc.blocks[2] = types.NewBlockWithHeader(h)
	if err := s.backfillManager(context.Background(), newAuthorization(), manager, 3); !errors.Is(err, ingest.ErrFatal) {
		t.Fatal("changed backfill anchor accepted", err)
	}
}

func TestMainnetInboxCommitmentReference(t *testing.T) {
	manifests, err := filepath.Glob("../blob/testdata/mainnet-*/manifest.json")
	if err != nil || len(manifests) == 0 {
		t.Fatal("missing mainnet fixtures", err)
	}
	for _, path := range manifests {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			dir := filepath.Dir(path)
			read := func(name string, dst any) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, dst); err != nil {
					t.Fatal(err)
				}
			}
			var manifest struct {
				Sources []struct {
					TransactionFile string `json:"transactionFile"`
				} `json:"sources"`
				Expected struct {
					Batch  uint64 `json:"batchIndex"`
					First  uint64 `json:"firstL2Block"`
					Blocks uint64 `json:"blockCount"`
				} `json:"expected"`
			}
			read("manifest.json", &manifest)
			var commit struct {
				Transaction types.Transaction `json:"transaction"`
				Block       types.Header      `json:"block"`
				Receipt     types.Receipt     `json:"receipt"`
			}
			read("commitment.json", &commit)
			if commit.Transaction.Hash() != commit.Receipt.TxHash || commit.Receipt.BlockHash != commit.Block.Hash() {
				t.Fatal("commitment provenance mismatch")
			}
			sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), &commit.Transaction)
			if err != nil {
				t.Fatal(err)
			}
			batch, hashes, err := parseSubmission(&commit.Transaction, types.NewBlockWithHeader(&commit.Block), sender)
			if err != nil {
				t.Fatal(err)
			}
			if batch.Index != manifest.Expected.Batch || batch.PrevTotalElements != manifest.Expected.First-1 || batch.Size != manifest.Expected.Blocks || batch.BlockNumber != commit.Block.Number.Uint64() {
				t.Fatal(batch)
			}
			files := []string{"blob-transaction.json"}
			if len(manifest.Sources) > 0 {
				files = nil
				for _, source := range manifest.Sources {
					files = append(files, source.TransactionFile)
				}
			}
			if len(hashes) != len(files) {
				t.Fatal("reference count", hashes)
			}
			for i, file := range files {
				var source struct {
					Transaction types.Transaction `json:"transaction"`
				}
				read(file, &source)
				if hashes[i] != source.Transaction.Hash() {
					t.Fatal("wrong Blob reference", hashes[i])
				}
			}
			if commit.Transaction.To() == nil || *commit.Transaction.To() != common.HexToAddress("0xff00000000000000000000000000000000001088") {
				t.Fatal("wrong Inbox")
			}
		})
	}
}

func TestSubmissionFiltersUnauthorizedBeforeChainValidation(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(fmt.Sprint("authorized=", authorized), func(t *testing.T) {
			s, rpc, _, _ := fixtureSyncer(t)
			keyByte := "02"
			if authorized {
				keyByte = "01"
			}
			key, err := crypto.HexToECDSA(strings.Repeat(keyByte, 32))
			if err != nil {
				t.Fatal(err)
			}
			tx, err := types.SignTx(types.NewTx(&types.LegacyTx{To: &s.Config.Inbox, Gas: 50000, GasPrice: big.NewInt(1), Data: []byte{3}}), types.HomesteadSigner{}, key)
			if err != nil {
				t.Fatal(err)
			}
			// No receipt exists: an unrelated sender must be ignored before any
			// receipt lookup, chain validation or parsing of the short calldata.
			err = s.submission(context.Background(), newAuthorization(), tx, rpc.blocks[2], 0, 0, &scanResult{})
			if authorized {
				if !errors.Is(err, ingest.ErrFatal) {
					t.Fatal("authorized submission bypassed chain validation", err)
				}
			} else if err != nil {
				t.Fatal("unauthorized legacy transaction halted ingestion", err)
			}
		})
	}
}
