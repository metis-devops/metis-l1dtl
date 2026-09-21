package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/server"
)

func TestHealthcheckStatus(t *testing.T) {
	for _, code := range []int{200, 204, 301, 404, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/readyz" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Location", "/healthz")
				w.WriteHeader(code)
			}))
			defer s.Close()
			err := runHealthcheck([]string{"--url=" + s.URL + "/readyz"})
			if (err == nil) != (code == 200) {
				t.Fatalf("status %d: %v", code, err)
			}
		})
	}
}

func TestHealthcheckArguments(t *testing.T) {
	for _, args := range [][]string{{"--timeout=0"}, {"--timeout=-1s"}, {"--timeout=invalid"}, {"--url=/healthz"}, {"--url=ftp://localhost"}, {"--url=http://"}, {"--unknown"}, {"extra"}} {
		if err := runHealthcheck(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if err := runHealthcheck([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

func TestHealthcheckTimeoutAndConnectionFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	start := time.Now()
	if err := runHealthcheck([]string{"--url=" + s.URL, "--timeout=50ms"}); err == nil {
		t.Fatal("timeout succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not bounded")
	}
	s.Close()
	if err := runHealthcheck([]string{"--url=" + s.URL}); err == nil {
		t.Fatal("closed server succeeded")
	}
}

func TestHealthcheckProcess(t *testing.T) {
	status := new(ingest.Status)
	s := httptest.NewServer((&server.Server{Status: status}).Handler())
	defer s.Close()
	for _, tc := range []struct {
		name, path string
		ready      bool
		fatal      error
		code       int
	}{
		{name: "live while syncing", path: "/healthz"},
		{name: "not ready", path: "/readyz", code: 1},
		{name: "ready", path: "/readyz", ready: true},
		{name: "halted readiness", path: "/readyz", fatal: errors.New("integrity failure"), code: 1},
		{name: "halted liveness", path: "/healthz", fatal: errors.New("integrity failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status.Set(tc.ready, tc.fatal)
			args, err := json.Marshal([]string{"healthcheck", "--url=" + s.URL + tc.path})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServiceProcess$")
			cmd.Env = append(os.Environ(), "L1DTL_TEST_PROCESS=1", "L1DTL_TEST_ARGS="+string(args))
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("process: %v", err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, output)
			}
		})
	}
}
