//go:build !unix

package api

// volumeSpace is unknown where there is no statfs.
func volumeSpace(string) (capacity, free int64) { return 0, 0 }
