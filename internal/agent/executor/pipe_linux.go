package executor

import (
	"os"
	"runtime"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// growPipe raises the capacity of the pipe or FIFO f to pipeSize. Failure is
// harmless; the pipe keeps its default size.
func growPipe(f *os.File) {
	_, _ = unix.FcntlInt(f.Fd(), unix.F_SETPIPE_SZ, pipeSize)
}

// relay moves everything from the pipe src to the pipe dst with splice(2), so
// the data never enters user space, and counts the bytes moved in n.
func relay(dst, src *os.File, n *atomic.Int64) error {
	defer runtime.KeepAlive(src)
	defer runtime.KeepAlive(dst)
	// Fd puts both files into blocking mode, so splice waits for data and room.
	rfd, wfd := int(src.Fd()), int(dst.Fd())
	for {
		m, err := unix.Splice(rfd, nil, wfd, nil, pipeSize, unix.SPLICE_F_MOVE|unix.SPLICE_F_MORE)
		if m > 0 {
			n.Add(m)
		}
		switch {
		case err == unix.EINTR:
		case err != nil:
			return err
		case m == 0:
			return nil
		}
	}
}
