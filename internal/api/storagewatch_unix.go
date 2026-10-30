//go:build unix

package api

import (
	"path/filepath"
	"syscall"
)

// volumeFree is the space left for a non-root writer on the volume holding path, and the volume's size.
func volumeFree(path string) (freeBytes, totalBytes uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}
