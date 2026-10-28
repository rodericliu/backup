//go:build !linux

package executor

import (
	"io"
	"os"
	"sync/atomic"
)

func growPipe(*os.File) {}

// relay copies src to dst and counts the bytes copied in n.
func relay(dst, src *os.File, n *atomic.Int64) error {
	return drain(io.MultiWriter(dst, counter{n}), src)
}
