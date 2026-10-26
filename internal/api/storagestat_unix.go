//go:build unix

package api

import "syscall"

// volumeSpace is the size of the volume holding dir and the part of it free to an unprivileged process.
func volumeSpace(dir string) (capacity, free int64) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return 0, 0
	}
	bs := int64(fs.Bsize)
	return int64(fs.Blocks) * bs, int64(fs.Bavail) * bs
}
