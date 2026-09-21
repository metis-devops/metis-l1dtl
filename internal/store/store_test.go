package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

func TestMissingCommittedRecords(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"), Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	zero, one, future := uint64(0), uint64(1), uint64(2)
	for _, index := range []*uint64{nil, &zero} {
		if got, err := s.Get(index); got != nil || err != nil {
			t.Fatalf("empty database: %v, %v", got, err)
		}
	}
	if err := s.Commit(State{}, State{Height: &one}, []Record{{Index: 0}, {Index: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, index := range []uint64{zero, one} {
		if err := s.db.Delete(key(index), pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for _, index := range []*uint64{nil, &zero, &one} {
		if got, err := s.Get(index); got != nil || !errors.Is(err, ErrIntegrity) {
			t.Fatalf("missing committed record: %v, %v", got, err)
		}
	}
	if got, err := s.Get(&future); got != nil || err != nil {
		t.Fatalf("future index: %v, %v", got, err)
	}
}

func TestReopenIdentityAndConflict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	id := Identity{Version: 1, L1: 1, L2: 1088}
	s, e := Open(p, id)
	if e != nil {
		t.Fatal(e)
	}
	h := uint64(5)
	r := Record{Index: 0, Data: "0x", GasLimit: "1"}
	if e = s.Commit(State{}, State{Height: &h}, []Record{r}); e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	s, e = Open(p, id)
	if e != nil {
		t.Fatal(e)
	}
	st, _ := s.State()
	if *st.Latest != 0 || *st.Height != 5 {
		t.Fatal(st)
	}
	if e = s.Commit(st, st, []Record{r}); e != nil {
		t.Fatal(e)
	}
	r.Data = "0x01"
	if e = s.Commit(st, st, []Record{r}); !errors.Is(e, ErrIntegrity) {
		t.Fatal(e)
	}
	_ = s.Close()
	id.L2++
	if db, e := Open(p, id); !errors.Is(e, ErrIntegrity) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal(e)
	}
}

func TestBatchReadYourWritesAndRollback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"), Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h := uint64(1)
	r := Record{Index: 0, Data: "0x"}
	conflict := r
	conflict.Data = "0xff"
	if err := s.Commit(State{}, State{Height: &h}, []Record{r, conflict}); !errors.Is(err, ErrIntegrity) {
		t.Fatal(err)
	}
	st, err := s.State()
	if err != nil || st.Height != nil {
		t.Fatalf("partial checkpoint: %+v %v", st, err)
	}
	zero := uint64(0)
	got, err := s.Get(&zero)
	if err != nil || got != nil {
		t.Fatalf("partial record: %+v %v", got, err)
	}
	if err := s.Commit(State{}, State{Height: &h}, []Record{r, r}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(nil)
	if err != nil || got == nil || got.Index != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestConcurrentCheckpointComparison(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"), Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			h := uint64(1)
			results <- s.Commit(State{}, State{Height: &h}, []Record{{Index: 0}})
		})
	}
	wg.Wait()
	close(results)
	success, stale := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrIntegrity) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
}

func TestFileRejectedAndExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	data := []byte("existing bbolt file")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path, Identity{}); err == nil {
		_ = s.Close()
		t.Fatal("opened file as directory")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatal("existing file changed")
	}
	path = filepath.Join(t.TempDir(), "pebble")
	s, err := Open(path, Identity{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if other, err := Open(path, Identity{}); err == nil {
		_ = other.Close()
		t.Fatal("second opener acquired database")
	}
}
