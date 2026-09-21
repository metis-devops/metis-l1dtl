//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var repoRoot, serviceBinary string

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

func runTests(m *testing.M) (code int) {
	var err error
	repoRoot, err = filepath.Abs("../..")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := os.MkdirTemp("", "l1dtl-e2e-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "remove E2E binary:", err)
			code = 1
		}
	}()
	serviceBinary = filepath.Join(dir, "metis-l1dtl")
	goTool := os.Getenv("L1DTL_E2E_GO")
	if goTool == "" {
		goTool = "go"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goTool, "build", "-race", "-o", serviceBinary, "./cmd/metis-l1dtl")
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = repoRoot
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build E2E service:", err)
		return 1
	}
	return m.Run()
}

func eventually(t *testing.T, description string, check func() (bool, error)) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		ok, err := check()
		if ok && err == nil {
			return
		}
		last = err
		select {
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s: %v", description, last)
		case <-t.Context().Done():
			t.Fatalf("canceled waiting for %s: %v", description, last)
		case <-ticker.C:
		}
	}
}

type composeProject struct{ name string }

func (p composeProject) run(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	base := []string{"compose", "-f", filepath.Join(repoRoot, "compose.e2e.yaml"), "-p", p.name}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func startAnvil(t *testing.T) string {
	t.Helper()
	p := composeProject{name: "l1dtl-e2e-" + strings.ToLower(filepath.Base(t.TempDir())) + fmt.Sprintf("-%d", time.Now().UnixNano())}
	t.Cleanup(func() {
		if t.Failed() {
			out, err := p.run(10*time.Second, "logs", "--no-color", "anvil")
			t.Logf("Anvil logs (%v):\n%s", err, out)
		}
		out, err := p.run(30*time.Second, "down", "--volumes", "--remove-orphans", "--timeout", "5")
		if err != nil {
			t.Errorf("Compose cleanup %s: %v\n%s", p.name, err, out)
		}
		out, err = p.run(10*time.Second, "ps", "--all", "--quiet")
		if err != nil || strings.TrimSpace(out) != "" {
			t.Errorf("Compose resources remain for %s: %v\n%s", p.name, err, out)
		}
	})
	if out, err := p.run(3*time.Minute, "up", "-d", "anvil"); err != nil {
		t.Fatalf("start Anvil (Docker Engine and Compose required): %v\n%s", err, out)
	}
	out, err := p.run(10*time.Second, "port", "anvil", "8545")
	if err != nil {
		t.Fatalf("Anvil port: %v\n%s", err, out)
	}
	address := strings.TrimSpace(out)
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("unexpected Anvil port mapping %q: %v", address, err)
	}
	return "http://" + address
}

type logBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

type service struct {
	t       *testing.T
	cmd     *exec.Cmd
	logs    logBuffer
	done    chan struct{}
	err     error // Read only after done is closed.
	stopped bool
	url     string
}

func startService(t *testing.T, a *chain, db string, start, confirmations uint64) *service {
	t.Helper()
	s := &service{t: t, done: make(chan struct{})}
	s.cmd = exec.Command(serviceBinary,
		"--l1-rpc="+a.url, "--l1-chain-id=31337", "--l2-chain-id=1088",
		"--address-manager="+a.manager.Hex(), fmt.Sprintf("--l1-start-height=%d", start),
		fmt.Sprintf("--confirmations=%d", confirmations), "--batch-size=3",
		"--poll-interval=25ms", "--listen=127.0.0.1:0", "--db="+db)
	s.cmd.Stdout, s.cmd.Stderr = &s.logs, &s.logs
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.err = s.cmd.Wait(); close(s.done) }()
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("service logs:\n%s", s.logs.String())
		}
	})
	eventually(t, "service listener", func() (bool, error) {
		select {
		case <-s.done:
			t.Fatalf("service exited: %v\n%s", s.err, s.logs.String())
		default:
		}
		for raw := range strings.SplitSeq(s.logs.String(), "\n") {
			var line struct {
				Msg    string `json:"msg"`
				Listen string `json:"listen"`
			}
			if json.Unmarshal([]byte(raw), &line) == nil && line.Msg == "service started" {
				s.url = "http://" + line.Listen
				return true, nil
			}
		}
		return false, nil
	})
	return s
}

func (s *service) stop() {
	s.t.Helper()
	if s.stopped {
		return
	}
	s.stopped = true
	select {
	case <-s.done:
		s.t.Errorf("service exited before shutdown: %v", s.err)
		return
	default:
	}
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		s.t.Errorf("SIGTERM: %v", err)
	}
	select {
	case <-s.done:
		if s.err != nil {
			s.t.Errorf("service shutdown: %v", s.err)
		}
	case <-time.After(15 * time.Second):
		if err := s.cmd.Process.Kill(); err != nil {
			s.t.Errorf("kill service: %v", err)
		}
		<-s.done
		s.t.Error("service did not exit after SIGTERM")
	}
}
