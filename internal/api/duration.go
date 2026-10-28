// Package api defines types shared by backup-server and backup-agent.
package api

import (
	"fmt"
	"time"
)

// Duration is a time.Duration that encodes as a Go duration string
// (for example "4h" or "90s") in both YAML and JSON.
type Duration time.Duration

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}
