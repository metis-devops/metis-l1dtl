package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// runHealthcheck probes the running service without opening its database or RPC.
func runHealthcheck(args []string) error {
	f := flag.NewFlagSet("metis-l1dtl healthcheck", flag.ContinueOnError)
	endpoint := f.String("url", "http://127.0.0.1:7878/healthz", "HTTP(S) health or readiness endpoint")
	timeout := f.Duration("timeout", 3*time.Second, "request timeout (must be positive)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *timeout <= 0 {
		return fmt.Errorf("healthcheck requires a positive --timeout and no positional arguments")
	}
	u, err := url.Parse(*endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("healthcheck --url must be an absolute HTTP(S) URL")
	}
	// Probes must reach the target directly, independent of proxy environment.
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   *timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(*endpoint)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: unexpected HTTP status %s", resp.Status)
	}
	return nil
}
