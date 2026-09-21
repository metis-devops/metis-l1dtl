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
