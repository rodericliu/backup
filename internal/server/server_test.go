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
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/store/storetest"
)

func TestMain(m *testing.M) {
	crypto.PasswordCost = bcrypt.MinCost
	os.Exit(m.Run())
}

type testEnv struct {
	t     *testing.T
	store *store.Store
	srv   *Server
	web   *httptest.Server
	agent *httptest.Server
	clock *clock
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// browser returns a client of the web listener with its own cookie jar.
func (e *testEnv) browser() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Client{Transport: e.web.Client().Transport, Jar: jar}
}

// api returns the URL of a web API path.
func (e *testEnv) api(path string) string { return e.web.URL + WebAPIPrefix + path }

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
	e := &testEnv{t: t, store: st, clock: &clock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}}
	e.srv = New(Options{Store: st, Sealer: sealer, Config: cfg, Logger: slog.New(slog.DiscardHandler), Now: e.clock.Now})
	e.web = httptest.NewTLSServer(e.srv.WebHandler())
	e.agent = httptest.NewTLSServer(e.srv.AgentHandler())
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
	for _, ts := range []*httptest.Server{e.web, e.agent} {
		var h health
		if code := do(t, ts.Client(), "GET", ts.URL+"/healthz", nil, &h); code != http.StatusOK || h.Status != "ok" {
			t.Errorf("%s/healthz = %d %+v", ts.URL, code, h)
		}
	}
	e.store.Close()
	var h health
	if code := do(t, e.web.Client(), "GET", e.web.URL+"/healthz", nil, &h); code != http.StatusServiceUnavailable || h.Status != "unavailable" {
		t.Errorf("healthz without database = %d %+v", code, h)
	}
}

func TestErrors(t *testing.T) {
	e := newTestEnv(t)
	resp, err := e.web.Client().Get(e.web.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing on the web listener")
	}
	if code := do(t, e.web.Client(), "GET", e.web.URL+AgentAPIPrefix+"/config", nil, nil); code != http.StatusNotFound {
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

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
