package config

import (
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	args := []string{"--l1-rpc=http://localhost:8545", "--l1-chain-id=1", "--l2-chain-id=1088", "--address-manager=0x0000000000000000000000000000000000000001", "--l1-start-height=0"}
	c, e := Parse(args)
	if e != nil || c.Confirmations != 35 || c.BatchSize != 2000 || c.Poll != 5*time.Second {
		t.Fatalf("%+v %v", c, e)
	}
	for _, extra := range []string{"--batch-size=0", "--rpc-batch-size=100", "--poll-interval=0s", "--l2-chain-id=0", "--address-manager=bad"} {
		if _, e := Parse(append(append([]string{}, args...), extra)); e == nil {
			t.Fatal(extra)
		}
	}
	if _, e := Parse(args[:4]); e == nil {
		t.Fatal("missing start accepted")
	}
}

func TestBlobFlagsTogether(t *testing.T) {
	base := []string{"--l1-rpc=http://localhost:8545", "--l1-chain-id=1", "--l2-chain-id=1088", "--address-manager=0x0000000000000000000000000000000000000001", "--l1-start-height=0"}
	blob := []string{"--l1-beacon=http://localhost:5052", "--batch-inbox-address=0x0000000000000000000000000000000000000002", "--batch-inbox-l1-height=0", "--batch-inbox-sender=0x0000000000000000000000000000000000000003", "--batch-inbox-blob-sender=0x0000000000000000000000000000000000000004"}
	for mask := 1; mask < 32; mask++ {
		args := append([]string{}, base...)
		for i, v := range blob {
			if mask&(1<<i) != 0 {
				args = append(args, v)
			}
		}
		c, err := Parse(args)
		if mask == 31 {
			if err != nil || !c.BlobEnabled() {
				t.Fatal(c, err)
			}
		} else if err == nil {
			t.Fatalf("accepted partial flags %d", mask)
		}
	}
	for _, bad := range []string{"--l1-beacon=ftp://beacon", "--l1-beacon=", "--batch-inbox-sender=0x0000000000000000000000000000000000000000"} {
		args := append(append(append([]string{}, base...), blob...), bad)
		if _, err := Parse(args); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
