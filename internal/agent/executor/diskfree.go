package executor

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// FreeSpace returns the bytes available to unprivileged users on the
// filesystem containing path.
func FreeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), nil
}
