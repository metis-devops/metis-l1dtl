package blobingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
)

var senderManagerName = crypto.Keccak256Hash([]byte("Proxy__MVM_InboxSenderManager"))
var senderABI = func() abi.ABI {
	a, err := abi.JSON(strings.NewReader(`[{"type":"event","name":"InboxSenderSet","inputs":[{"name":"blockNumber","type":"uint256","indexed":true},{"name":"inboxSender","type":"address","indexed":true},{"name":"inboxSenderType","type":"uint8","indexed":true}]}]`))
	if err != nil {
		panic(err)
	}
	return a
}()
var senderTopic = senderABI.Events["InboxSenderSet"].ID

type senderSchedule struct {
	Batch map[uint64]common.Address
	Blob  map[uint64]common.Address
}
type authorization struct {
	Manager common.Address
	Known   map[common.Address]*senderSchedule
}

func newAuthorization() *authorization {
	return &authorization{Known: map[common.Address]*senderSchedule{}}
}
func (a *authorization) ensure(address common.Address) {
	if a.Known[address] == nil {
		a.Known[address] = &senderSchedule{Batch: map[uint64]common.Address{}, Blob: map[uint64]common.Address{}}
	}
}
func (a *authorization) sender(height uint64, blob bool, fallback common.Address) common.Address {
	table := a.Known[a.Manager]
	if table == nil {
		return fallback
	}
	entries := table.Batch
	if blob {
		entries = table.Blob
	}
	latest := uint64(0)
	found := false
	for effective, address := range entries {
		if effective <= height && (!found || effective >= latest) {
			latest, found, fallback = effective, true, address
		}
	}
	return fallback
}
func decodeAuth(raw json.RawMessage) (*authorization, error) {
	a := newAuthorization()
	if len(raw) == 0 {
		return a, nil
	}
	if err := json.Unmarshal(raw, a); err != nil || a.Known == nil {
		return nil, fatal("invalid stored authorization")
	}
	for address, schedule := range a.Known {
		if address == (common.Address{}) || schedule == nil || schedule.Batch == nil || schedule.Blob == nil {
			return nil, fatal("invalid stored sender schedule")
		}
	}
	if a.Manager != (common.Address{}) && a.Known[a.Manager] == nil {
		return nil, fatal("missing stored active manager")
	}
	for _, schedule := range a.Known {
		for _, entries := range []map[uint64]common.Address{schedule.Batch, schedule.Blob} {
			for _, sender := range entries {
				if sender == (common.Address{}) {
					return nil, fatal("zero stored sender")
				}
			}
		}
	}
	return a, nil
}
func positionLess(a, b types.Log) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	if a.TxIndex != b.TxIndex {
		return a.TxIndex < b.TxIndex
	}
	return a.Index < b.Index
}
func validateLog(l types.Log, from, end uint64) error {
	if l.Removed || l.BlockNumber < from || l.BlockNumber > end || len(l.Topics) == 0 {
		return fatal("invalid authorization log range or metadata")
	}
	return nil
}
func addressChange(l types.Log) (common.Address, common.Address, error) {
	event := ingest.ContractABI.Events["AddressSet"]
	if len(l.Topics) != 2 || l.Topics[0] != event.ID || l.Topics[1] != senderManagerName {
		return common.Address{}, common.Address{}, fatal("invalid sender manager event")
	}
	values, err := event.Inputs.NonIndexed().Unpack(l.Data)
	if err != nil {
		return common.Address{}, common.Address{}, fatal("invalid sender manager ABI")
	}
	raw, err := event.Inputs.NonIndexed().Pack(values...)
	if err != nil || !bytes.Equal(raw, l.Data) {
		return common.Address{}, common.Address{}, fatal("noncanonical sender manager ABI")
	}
	return values[0].(common.Address), values[1].(common.Address), nil
}
func (a *authorization) apply(l types.Log, managerAddress common.Address) error {
	if l.Address == managerAddress && l.Topics[0] == ingest.ContractABI.Events["AddressSet"].ID {
		next, old, err := addressChange(l)
		if err != nil {
			return err
		}
		if old != a.Manager {
			return fatal("sender manager address history mismatch")
		}
		a.Manager = next
		return nil
	}
	if len(l.Topics) != 4 || l.Topics[0] != senderTopic || len(l.Data) != 0 {
		return fatal("invalid InboxSenderSet topics/data")
	}
	height := l.Topics[1].Big()
	kind := l.Topics[3].Big()
	if !height.IsUint64() || !kind.IsUint64() || kind.Uint64() > 1 || !bytes.Equal(l.Topics[2][:12], make([]byte, 12)) {
		return fatal("invalid InboxSenderSet values")
	}
	sender := common.BytesToAddress(l.Topics[2][12:])
	if sender == (common.Address{}) {
		return fatal("zero Inbox sender")
	}
	table := a.Known[l.Address]
	if table == nil {
		return fatal("unknown sender manager log")
	}
	if kind.Uint64() == 0 {
		table.Batch[height.Uint64()] = sender
	} else {
		table.Blob[height.Uint64()] = sender
	}
	return nil
}

// authLogs discovers address changes first, and backfills newly discovered
// managers before their first use. No eth_call or per-event header reads.
func (s *Syncer) authLogs(ctx context.Context, a *authorization, from, end uint64) ([]types.Log, error) {
	changes, err := s.RPC.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: number(from), ToBlock: number(end), Addresses: []common.Address{s.Config.AddressManager}, Topics: [][]common.Hash{{ingest.ContractABI.Events["AddressSet"].ID}, {senderManagerName}}})
	if err != nil {
		return nil, err
	}
	for _, l := range changes {
		if err := validateLog(l, from, end); err != nil {
			return nil, err
		}
		if l.Address != s.Config.AddressManager {
			return nil, fatal("wrong AddressManager log")
		}
		next, _, err := addressChange(l)
		if err != nil {
			return nil, err
		}
		if next == (common.Address{}) || a.Known[next] != nil {
			continue
		}
		if err := s.backfillManager(ctx, a, next, from); err != nil {
			return nil, err
		}
	}
	addresses := make([]common.Address, 0, len(a.Known))
	for address := range a.Known {
		addresses = append(addresses, address)
	}
	if len(addresses) > 0 {
		logs, err := s.senderLogs(ctx, addresses, from, end)
		if err != nil {
			return nil, err
		}
		changes = append(changes, logs...)
	}
	return orderedLogs(changes)
}
func (s *Syncer) senderLogs(ctx context.Context, addresses []common.Address, from, end uint64) ([]types.Log, error) {
	logs, err := s.RPC.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: number(from), ToBlock: number(end), Addresses: addresses, Topics: [][]common.Hash{{senderTopic}}})
	if err != nil {
		return nil, err
	}
	allowed := map[common.Address]bool{}
	for _, address := range addresses {
		allowed[address] = true
	}
	for _, l := range logs {
		if err := validateLog(l, from, end); err != nil {
			return nil, err
		}
		if !allowed[l.Address] || l.Topics[0] != senderTopic {
			return nil, fatal("unexpected sender log")
		}
	}
	return logs, nil
}
func orderedLogs(logs []types.Log) ([]types.Log, error) {
	sort.Slice(logs, func(i, j int) bool { return positionLess(logs[i], logs[j]) })
	seen := map[string][]byte{}
	out := make([]types.Log, 0, len(logs))
	for _, l := range logs {
		id := fmt.Sprintf("%d/%d", l.BlockNumber, l.Index)
		raw, _ := json.Marshal(l)
		if old, ok := seen[id]; ok {
			if !bytes.Equal(old, raw) {
				return nil, fatal("conflicting authorization log")
			}
			continue
		}
		seen[id] = raw
		out = append(out, l)
	}
	return out, nil
}

// Backfill pages survive transient failures in memory, but are never published
// as scan progress. Each page is tied to the block immediately before the scan.
var errBackfillPending = errors.New("sender history backfill pending")

type senderBackfill struct {
	before, next uint64
	anchor       common.Hash
	auth         *authorization
}

func (s *Syncer) backfillManager(ctx context.Context, a *authorization, address common.Address, before uint64) error {
	if before == 0 {
		a.ensure(address)
		return nil
	}
	if s.backfills == nil {
		s.backfills = map[common.Address]*senderBackfill{}
	}
	progress := s.backfills[address]
	if progress == nil || progress.before != before {
		h, err := s.header(ctx, before-1)
		if err != nil {
			return err
		}
		initial := newAuthorization()
		initial.ensure(address)
		progress = &senderBackfill{before: before, anchor: h.Hash(), auth: initial}
		s.backfills[address] = progress
	}
	if err := s.check(ctx, before-1, progress.anchor); err != nil {
		return err
	}
	if progress.next < before {
		stop := before - 1
		if stop-progress.next >= s.Config.BatchSize {
			stop = progress.next + s.Config.BatchSize - 1
		}
		updated, err := cloneAuth(progress.auth)
		if err != nil {
			return err
		}
		prior, err := s.senderLogs(ctx, []common.Address{address}, progress.next, stop)
		if err != nil {
			return err
		}
		prior, err = orderedLogs(prior)
		if err != nil {
			return err
		}
		for _, l := range prior {
			if err := updated.apply(l, s.Config.AddressManager); err != nil {
				return err
			}
		}
		if err := s.check(ctx, before-1, progress.anchor); err != nil {
			return err
		}
		progress.auth, progress.next = updated, stop+1
	}
	if progress.next < before {
		return errBackfillPending
	}
	// The current scan may add later events; do not mutate cached history.
	copy, err := cloneAuth(progress.auth)
	if err != nil {
		return err
	}
	a.Known[address] = copy.Known[address]
	return nil
}
