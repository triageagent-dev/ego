//go:build !unix

package api

import "errors"

func volumeFree(string) (uint64, uint64, error) {
	return 0, 0, errors.New("free space is not measured on this platform")
}
