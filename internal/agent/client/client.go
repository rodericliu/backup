// Package client is the agent side of the agent API (PRD 8.1). All requests
// go from the agent to the server over HTTPS verified against the server CA.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
)

// requestTimeout bounds requests other than long polls.
const requestTimeout = 30 * time.Second

// Error is an error response of the server.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("server: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsTokenInvalid reports whether err means the agent token is unknown or
// revoked.
func IsTokenInvalid(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Code == api.CodeTokenInvalid
}

// IsAPIVersion reports whether err means the server speaks another API version.
func IsAPIVersion(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Code == api.CodeAPIVersion
}

// IsRejected reports whether the server rejected a request for good, so that
// retrying the same request is pointless (4xx other than authentication and
// version errors).
func IsRejected(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status >= 400 && e.Status < 500 && e.Code != api.CodeTokenInvalid && e.Code != api.CodeAPIVersion
}

// Client calls the agent API.
type Client struct {
	base  string
	http  *http.Client
	token string
}

// New returns a client of the server at serverURL (https://host:port). caFile
// is the PEM CA certificate that signed the server certificate.
func New(serverURL, caFile string) (*Client, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("server_url %q: want https://host:port", serverURL)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("server_ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("server_ca %s: no PEM certificate", caFile)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return NewWithHTTPClient(serverURL, &http.Client{Transport: tr}), nil
}

// NewWithHTTPClient returns a client using hc, for tests.
func NewWithHTTPClient(serverURL string, hc *http.Client) *Client {
	return &Client{base: strings.TrimRight(serverURL, "/") + "/api/agent/v1", http: hc}
}

// SetToken sets the agent token sent with every request.
func (c *Client) SetToken(token string) { c.token = token }

// do sends a request with a JSON body (or raw bytes) and decodes a JSON
// response into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		rd    io.Reader
		ctype string
	)
	switch b := body.(type) {
	case nil:
	case []byte:
		rd, ctype = bytes.NewReader(b), "text/plain; charset=utf-8"
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(api.HeaderAPIVersion, strconv.Itoa(api.AgentAPIVersion))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var eb struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &eb)
		return &Error{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// Register exchanges a registration token for an agent token (PRD H-2).
func (c *Client) Register(ctx context.Context, req *api.RegisterRequest) (*api.RegisterResponse, error) {
	var resp api.RegisterResponse
	if err := c.do(ctx, "POST", "/register", req, &resp, requestTimeout); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Heartbeat sends a heartbeat and returns the newest configuration version.
func (c *Client) Heartbeat(ctx context.Context, hb *api.Heartbeat) (*api.HeartbeatResponse, error) {
	var resp api.HeartbeatResponse
	if err := c.do(ctx, "POST", "/heartbeat", hb, &resp, requestTimeout); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Config fetches the host configuration.
func (c *Client) Config(ctx context.Context) (*api.AgentConfig, error) {
	var cfg api.AgentConfig
	if err := c.do(ctx, "GET", "/config", nil, &cfg, requestTimeout); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// JobEvents reports a job's state and events.
func (c *Client) JobEvents(ctx context.Context, id uuid.UUID, ev *api.JobEvents) error {
	return c.do(ctx, "POST", "/jobs/"+id.String()+"/events", ev, nil, requestTimeout)
}

// PutJob reports a job's final result.
func (c *Client) PutJob(ctx context.Context, j *api.Job) error {
	return c.do(ctx, "PUT", "/jobs/"+j.ID.String(), j, nil, requestTimeout)
}

// UploadLog uploads a job's complete log.
func (c *Client) UploadLog(ctx context.Context, id uuid.UUID, log []byte) error {
	return c.do(ctx, "POST", "/jobs/"+id.String()+"/log", log, nil, 5*time.Minute)
}

// Commands waits up to wait for commands.
func (c *Client) Commands(ctx context.Context, wait time.Duration) ([]api.Command, error) {
	var cmds []api.Command
	if err := c.do(ctx, "GET", "/commands?wait="+wait.String(), nil, &cmds, wait+requestTimeout); err != nil {
		return nil, err
	}
	return cmds, nil
}

// CommandResult reports the result of a command.
func (c *Client) CommandResult(ctx context.Context, id int64, res *api.CommandResult) error {
	return c.do(ctx, "POST", "/commands/"+strconv.FormatInt(id, 10)+"/result", res, nil, requestTimeout)
}
