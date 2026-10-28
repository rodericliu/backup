package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	json "encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/store/storetest"
)

type testEnv struct {
	t     *testing.T
	store *store.Store
	srv   *Server
	web   *httptest.Server
	agent *httptest.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st := storetest.New(t)
	cfg, err := config.Parse([]byte("timezone: UTC"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{1}, crypto.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, store: st}
	e.srv = New(Options{Store: st, Sealer: sealer, Config: cfg, Logger: slog.New(slog.DiscardHandler)})
	e.web = httptest.NewServer(e.srv.WebHandler())
	e.agent = httptest.NewServer(e.srv.AgentHandler())
	t.Cleanup(e.web.Close)
	t.Cleanup(e.agent.Close)
	return e
}

// do sends a JSON request and decodes a JSON response into out when out is
// not nil. It returns the response status.
func do(t *testing.T, c *http.Client, method, url string, body, out any, header ...string) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, url, b, err)
		}
	}
	return resp.StatusCode
}

func TestHealthz(t *testing.T) {
	e := newTestEnv(t)
	for _, base := range []string{e.web.URL, e.agent.URL} {
		var h health
		if code := do(t, nil, "GET", base+"/healthz", nil, &h); code != http.StatusOK || h.Status != "ok" {
			t.Errorf("%s/healthz = %d %+v", base, code, h)
		}
	}
	e.store.Close()
	var h health
	if code := do(t, nil, "GET", e.web.URL+"/healthz", nil, &h); code != http.StatusServiceUnavailable || h.Status != "unavailable" {
		t.Errorf("healthz without database = %d %+v", code, h)
	}
}

func TestErrors(t *testing.T) {
	e := newTestEnv(t)
	resp, err := http.Get(e.web.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing on the web listener")
	}
	if code := do(t, nil, "GET", e.web.URL+AgentAPIPrefix+"/config", nil, nil); code != http.StatusNotFound {
		t.Errorf("agent API on the web listener = %d, want 404", code)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRun(t *testing.T) {
	e := newTestEnv(t)
	dir := t.TempDir()
	now := time.Now()
	if err := crypto.InitCA(filepath.Join(dir, "ca"), "test CA", now); err != nil {
		t.Fatal(err)
	}
	if err := crypto.IssueServerCert(filepath.Join(dir, "ca"), filepath.Join(dir, "tls"), []string{"127.0.0.1"}, now); err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := LoadTLS(filepath.Join(dir, "tls", crypto.ServerCertFile), filepath.Join(dir, "tls", crypto.ServerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca", crypto.CACertFile))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	for _, split := range []bool{false, true} {
		e.srv.cfg.Listen = freePort(t)
		e.srv.cfg.AgentListen = ""
		if split {
			e.srv.cfg.AgentListen = freePort(t)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- e.srv.Run(ctx, tlsCfg) }()

		addrs := []string{e.srv.cfg.Listen}
		if split {
			addrs = append(addrs, e.srv.cfg.AgentListen)
		}
		for _, addr := range addrs {
			deadline := time.Now().Add(5 * time.Second)
			for {
				resp, err := client.Get("https://" + addr + "/healthz")
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						t.Errorf("https://%s/healthz = %d", addr, resp.StatusCode)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("server did not start on %s: %v", addr, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
