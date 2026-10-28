package executor

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// pipeSize is the requested capacity of the pipes on the data path and the
// size of a single transfer. Large transfers keep the number of system calls
// low at hundreds of MB/s.
const pipeSize = 1 << 20

// openFIFOReader opens the read end of a FIFO without waiting for a writer and
// returns it in blocking mode.
func openFIFOReader(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// drain copies r to w using pipeSize reads. Unlike io.Copy it does not fall
// back to a 32 KiB buffer when r is an *os.File.
func drain(w io.Writer, r *os.File) error {
	buf := make([]byte, pipeSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
