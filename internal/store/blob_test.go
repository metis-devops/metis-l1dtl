package store

import (
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/ethereum/go-ethereum/common"
)

func blobStore(t *testing.T) (*Store, BlobIdentity) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "db"), Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id := BlobIdentity{Version: 1, Start: 1, Inbox: common.HexToAddress("0x01"), BatchSender: common.HexToAddress("0x02"), BlobSender: common.HexToAddress("0x03")}
	if err := db.InitBlob(id); err != nil {
		t.Fatal(err)
	}
	return db, id
}
func channelFixture(index, ts uint64) (BlobTransaction, BlobChannel) {
	hash := common.BigToHash(new(big.Int).SetUint64(index + 1))
	tx := BlobTransaction{Hash: hash, Timestamp: ts}
	b := Block{Index: index, BatchIndex: index, Transactions: []BlockTransaction{}, Confirmed: true}
	ch := BlobChannel{Record: ChannelRecord{ID: fmt.Sprint(index), Sources: []common.Hash{hash}, Blocks: []uint64{index}, Batch: BlockBatch{Index: index}, ExpiresAt: ts}, Blocks: []Block{b}}
	return tx, ch
}
func TestBlobRetentionNoCountLimitAndAtomicity(t *testing.T) {
	db, _ := blobStore(t)
	before, err := db.BlobState()
	if err != nil {
		t.Fatal(err)
	}
	var txs []BlobTransaction
	var channels []BlobChannel
	for i := range uint64(130) {
		tx, ch := channelFixture(i, 100+i)
		txs = append(txs, tx)
		channels = append(channels, ch)
	}
	height := uint64(10)
	after := before
	after.Height = &height
	if err := db.CommitBlob(before, after, txs, channels); err != nil {
		t.Fatal(err)
	}
	out, err := db.GetBlock(nil)
	if err != nil || out.Block.Index != 129 {
		t.Fatal(out, err)
	}
	zero := uint64(0)
	if out, err := db.GetBlock(&zero); err != nil || out.Block == nil {
		t.Fatal("128 cap", err)
	}
	before, _ = db.BlobState()
	after = before
	after.Cutoff = 100
	if err := db.CommitBlob(before, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out, _ := db.GetBlock(&zero); out.Block == nil {
		t.Fatal("boundary expired")
	}
	before, _ = db.BlobState()
	after = before
	after.Cutoff = 101
	if err := db.CommitBlob(before, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out, _ := db.GetBlock(&zero); out.Block != nil {
		t.Fatal("old block retained")
	}
	st, _ := db.BlobState()
	if *st.Height != 10 {
		t.Fatal("prune moved scan checkpoint")
	}
	// A conflicting transaction must roll back other records and checkpoint.
	fresh, ch := channelFixture(200, 300)
	bad := txs[1]
	bad.Timestamp++
	after = st
	n := uint64(11)
	after.Height = &n
	if err := db.CommitBlob(st, after, []BlobTransaction{fresh, bad}, []BlobChannel{ch}); !errors.Is(err, ErrIntegrity) {
		t.Fatal(err)
	}
	i := uint64(200)
	if out, _ := db.GetBlock(&i); out.Block != nil {
		t.Fatal("partial commit")
	}
	state, _ := db.BlobState()
	if state.Revision != st.Revision || *state.Height != 10 {
		t.Fatal("advanced failed commit")
	}
	after = state
	after.Cutoff = 1000
	if err := db.CommitBlob(state, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := db.GetBlock(nil); err != nil || out.Block != nil || out.Batch != nil {
		t.Fatal(out, err)
	}
	if tx, err := db.BlobTransaction(txs[129].Hash); err != nil || tx != nil {
		t.Fatal("source not removed", err)
	}
}
func TestBlobDependencyAndMissingExpiration(t *testing.T) {
	db, _ := blobStore(t)
	tx, ch := channelFixture(9, 200)
	older, _ := channelFixture(8, 100)
	ch.Record.Sources = append(ch.Record.Sources, older.Hash)
	ch.Record.ExpiresAt = 100
	missing, _ := channelFixture(7, 100)
	missing.Missing = true
	st, _ := db.BlobState()
	if err := db.CommitBlob(st, st, []BlobTransaction{tx, older, missing}, []BlobChannel{ch}); err != nil {
		t.Fatal(err)
	}
	st, _ = db.BlobState()
	after := st
	after.Cutoff = 101
	if err := db.CommitBlob(st, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if out, err := db.GetBlock(nil); err != nil || out.Block != nil {
		t.Fatal("dependent block survived", err)
	}
	if got, err := db.BlobTransaction(missing.Hash); err != nil || got != nil {
		t.Fatal("missing marker survived", err)
	}
	if got, err := db.BlobTransaction(tx.Hash); err != nil || got == nil {
		t.Fatal("unexpired source removed", err)
	}
}
func TestBlobIdentityReopenAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	id := Identity{Version: 1}
	bid := BlobIdentity{Version: 1, Start: 4}
	db, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Commit(State{}, State{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InitBlob(bid); err != nil {
		t.Fatal(err)
	}
	tx, ch := channelFixture(8, 100)
	st, _ := db.BlobState()
	if err := db.CommitBlob(st, st, []BlobTransaction{tx}, []BlobChannel{ch}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.InitBlob(bid); err != nil {
		t.Fatal(err)
	}
	if out, err := db.GetBlock(nil); err != nil || out.Block == nil || out.Block.Index != 8 {
		t.Fatal(out, err)
	}
	changed := bid
	changed.BlobSender = common.HexToAddress("0x123")
	if err := db.InitBlob(changed); !errors.Is(err, ErrIntegrity) {
		t.Fatal("changed identity accepted", err)
	}
	if err := db.db.Delete(blobNumberKey("block", 8), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetBlock(nil); !errors.Is(err, ErrIntegrity) {
		t.Fatal("missing block hidden", err)
	}
	if err := db.db.Delete(blobIdentityKey, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := db.InitBlob(bid); !errors.Is(err, ErrIntegrity) {
		t.Fatal("missing identity repaired", err)
	}
}
func TestBlobConcurrentCheckpointAndReads(t *testing.T) {
	db, _ := blobStore(t)
	st, _ := db.BlobState()
	tx, ch := channelFixture(1, 100)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- db.CommitBlob(st, st, []BlobTransaction{tx}, []BlobChannel{ch}) })
	}
	wg.Wait()
	close(results)
	success, failed := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrIntegrity) {
			failed++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || failed != 1 {
		t.Fatal(success, failed)
	}
	for range 8 {
		wg.Go(func() {
			for range 50 {
				out, err := db.GetBlock(nil)
				if err != nil || (out.Block == nil) != (out.Batch == nil) {
					t.Errorf("inconsistent read %v", err)
				}
			}
		})
	}
	st, _ = db.BlobState()
	after := st
	after.Cutoff = 101
	if err := db.CommitBlob(st, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestBlockDepositHydration(t *testing.T) {
	for _, queue := range []uint64{0, 20396, 20397, 20398} {
		t.Run(fmt.Sprint(queue), func(t *testing.T) {
			db, _ := blobStore(t)
			source, channel := channelFixture(1, 100)
			channel.Blocks[0].Transactions = []BlockTransaction{
				{QueueOrigin: "l1", QueueIndex: &queue, Timestamp: 77, Value: "123", Confirmed: true},
				{QueueOrigin: "l1", QueueIndex: &queue},
				{QueueOrigin: "sequencer", Data: "0x1234"},
			}
			st, _ := db.BlobState()
			if err := db.CommitBlob(st, st, []BlobTransaction{source}, []BlobChannel{channel}); err != nil {
				t.Fatal(err)
			}
			index := uint64(1)
			for _, idx := range []*uint64{nil, &index} {
				out, err := db.GetBlock(idx)
				if err != nil || out.Block != nil || out.Batch != nil {
					t.Fatal("missing enqueue published placeholders", out, err)
				}
			}
			wantIndex := queue
			if wantIndex >= 20397 {
				wantIndex++
			}
			records := make([]Record, wantIndex+1)
			for i := range records {
				records[i].Index = uint64(i)
			}
			want := Enqueue{Index: wantIndex, Target: common.HexToAddress("0x1234"), Origin: common.HexToAddress("0x5678"), Data: "0xbeef", GasLimit: "65000", BlockNumber: 99, Timestamp: 88}
			records[wantIndex].Enqueue = want
			if err := db.Commit(State{}, State{}, records); err != nil {
				t.Fatal(err)
			}
			for _, idx := range []*uint64{nil, &index, nil} {
				out, err := db.GetBlock(idx)
				if err != nil || out.Block == nil || out.Batch == nil {
					t.Fatal(out, err)
				}
				for i, tx := range out.Block.Transactions[:2] {
					wantTimestamp := uint64(77)
					if i == 1 {
						wantTimestamp = want.Timestamp
					}
					if tx.QueueIndex == nil || *tx.QueueIndex != wantIndex || tx.Target != want.Target || tx.Origin != want.Origin || tx.Data != want.Data || tx.GasLimit != want.GasLimit || tx.BlockNumber != want.BlockNumber || tx.Timestamp != wantTimestamp {
						t.Fatalf("hydration mismatch: %+v", tx)
					}
				}
				if out.Block.Transactions[0].Value != "123" || !out.Block.Transactions[0].Confirmed || out.Block.Transactions[2].Data != "0x1234" {
					t.Fatal("unrelated fields changed", out.Block)
				}
			}
			// A hole behind the deposit checkpoint is corruption, not ingestion lag.
			if err := db.db.Delete(key(wantIndex), pebble.Sync); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetBlock(nil); !errors.Is(err, ErrIntegrity) {
				t.Fatal("committed enqueue hole hidden", err)
			}
		})
	}
}

func TestBlockDepositInvalidQueueIndex(t *testing.T) {
	max := ^uint64(0)
	for _, queue := range []*uint64{nil, &max} {
		db, _ := blobStore(t)
		source, channel := channelFixture(1, 100)
		channel.Blocks[0].Transactions = []BlockTransaction{{QueueOrigin: "l1", QueueIndex: queue}}
		st, _ := db.BlobState()
		if err := db.CommitBlob(st, st, []BlobTransaction{source}, []BlobChannel{channel}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetBlock(nil); !errors.Is(err, ErrIntegrity) {
			t.Fatal("invalid queue index accepted", err)
		}
	}
}
