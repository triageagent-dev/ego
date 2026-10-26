package api

import (
	"os"
	"path/filepath"
)

// storageStat is what the database takes on disk and what the volume under it can hold.
type storageStat struct {
	// DBBytes is the database file with its write-ahead log.
	DBBytes int64
	// Capacity and Free are the volume's size and what is left of it for this process; 0 when the system cannot say.
	Capacity, Free int64
}

// Storage measures the database on disk. ok is false for a store with no file to measure.
func (s *SQLiteStore) Storage() (st storageStat, ok bool) {
	if s == nil || s.path == "" {
		return st, false
	}
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(s.path + suffix); err == nil {
			st.DBBytes += fi.Size()
			ok = true
		}
	}
	st.Capacity, st.Free = volumeSpace(filepath.Dir(s.path))
	return st, ok
}
