// Package podman wraps the podman CLI used to inspect replica containers and
// run backup tools inside them.
package podman

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ErrNotFound is returned when a container does not exist.
var ErrNotFound = errors.New("container not found")

// waitDelay bounds how long a canceled command may keep its output pipes open
// after it was signaled.
const waitDelay = 30 * time.Second

// Client runs podman commands.
type Client struct {
	// Bin is the podman binary.
	Bin string
	// User, if set, is passed to `podman exec --user`.
	User string
}

// New returns a client using the given podman binary.
func New(bin string) *Client { return &Client{Bin: bin} }

// Mount is a bind mount of a container.
type Mount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Container describes a container as reported to the server (PRD H-6).
type Container struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Running bool     `json:"running"`
	Ports   []string `json:"ports"`
	Mounts  []Mount  `json:"mounts"`
}

// inspect is the subset of `podman container inspect` output we use.
type inspect struct {
	ID        string `json:"Id"`
	Name      string `json:"Name"`
	ImageName string `json:"ImageName"`
	State     struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

func (in *inspect) container() Container {
	c := Container{
		ID:      in.ID,
		Name:    strings.TrimPrefix(in.Name, "/"),
		Image:   in.ImageName,
		State:   in.State.Status,
		Running: in.State.Running,
		Ports:   []string{},
		Mounts:  []Mount{},
	}
	for port, bindings := range in.NetworkSettings.Ports {
		for _, b := range bindings {
			c.Ports = append(c.Ports, fmt.Sprintf("%s:%s->%s", b.HostIP, b.HostPort, port))
		}
	}
	slices.Sort(c.Ports)
	for _, m := range in.Mounts {
		if m.Type == "bind" || m.Type == "volume" {
			c.Mounts = append(c.Mounts, Mount{Source: m.Source, Destination: m.Destination})
		}
	}
	return c
}

// List returns all containers on the host, running or not.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, "ps", "--all", "--format", "{{.ID}}")
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return []Container{}, nil
	}
	containers, err := c.inspect(ctx, ids...)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	slices.SortFunc(containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
	return containers, nil
}

// Inspect returns one container, or an error wrapping ErrNotFound.
func (c *Client) Inspect(ctx context.Context, name string) (*Container, error) {
	containers, err := c.inspect(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	if len(containers) != 1 {
		return nil, fmt.Errorf("inspect %s: %w", name, ErrNotFound)
	}
	return &containers[0], nil
}

func (c *Client) inspect(ctx context.Context, names ...string) ([]Container, error) {
	out, err := c.run(ctx, append([]string{"container", "inspect", "--format", "json"}, names...)...)
	if err != nil {
		return nil, err
	}
	var raw []inspect
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode inspect output: %w", err)
	}
	containers := make([]Container, len(raw))
	for i := range raw {
		containers[i] = raw[i].container()
	}
	return containers, nil
}

// Exec returns a command running args inside container. When ctx is done the
// podman client receives SIGTERM; callers that must stop the process inside
// the container should also kill it there.
func (c *Client) Exec(ctx context.Context, container string, args ...string) *exec.Cmd {
	pre := []string{"exec"}
	if c.User != "" {
		pre = append(pre, "--user", c.User)
	}
	return c.command(ctx, append(append(pre, container), args...)...)
}

// Output runs args inside container and returns its stdout.
func (c *Client) Output(ctx context.Context, container string, args ...string) ([]byte, error) {
	return output(c.Exec(ctx, container, args...))
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = waitDelay
	return cmd
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return output(c.command(ctx, args...))
}

// ExitError is returned when a command exits unsuccessfully. Stderr holds the
// trimmed standard error output.
type ExitError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", strings.Join(e.Args, " "), e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func output(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		msg := strings.TrimSpace(stderr.String())
		e := &ExitError{Args: cmd.Args[1:], ExitCode: exitErr.ExitCode(), Stderr: msg}
		if strings.Contains(strings.ToLower(msg), "no such container") {
			return nil, errors.Join(ErrNotFound, e)
		}
		return nil, e
	}
	return nil, fmt.Errorf("run %s: %w", cmd.Path, err)
}
