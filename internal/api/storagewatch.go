package api

import (
	"fmt"
	"sync"
	"time"

	"github.com/triageagent-dev/ego/internal/logx"
)

// A full volume makes every archive write fail while ego keeps serving from memory, so nothing looks wrong until a
// restart drops what was never saved. The watch turns that into a notice to the Discord sinks, and a field in
// /api/health, the moment a snapshot fails or the volume runs low.
const (
	// storageLowFraction and storageLowBytes: free space below either one is "running out".
	storageLowFraction = 0.10
	storageLowBytes    = 256 << 20

	// A condition that lasts is repeated, not announced once and forgotten.
	storageFailRepeat = time.Hour
	storageLowRepeat  = 6 * time.Hour
)

type storageWatch struct {
	path   string
	notice func(text string)
	now    func() time.Time
	free   func(path string) (freeBytes, totalBytes uint64, err error)

	mu        sync.Mutex
	failing   bool
	failSince time.Time
	failErr   string
	failSent  time.Time
	lowSent   time.Time
	freeFrac  float64 // -1 until measured
}

func newStorageWatch(path string) *storageWatch {
	return &storageWatch{path: path, now: time.Now, free: volumeFree, freeFrac: -1}
}

func (w *storageWatch) send(text string) {
	if w.notice != nil {
		w.notice(text)
	}
}

// writeFailed records a failed archive write and says so on entering the state and every storageFailRepeat after.
func (w *storageWatch) writeFailed(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	w.failErr = err.Error()
	if !w.failing {
		w.failing, w.failSince = true, now
	}
	if now.Sub(w.failSent) < storageFailRepeat {
		return
	}
	w.failSent = now
	w.send(fmt.Sprintf("ego cannot write its session archive (since %s): %v. Sessions are held in memory only and are lost on a restart.",
		w.failSince.UTC().Format(time.RFC3339), err))
}

func (w *storageWatch) writeOK() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failing {
		return
	}
	w.send(fmt.Sprintf("ego writes its session archive again (it failed since %s).", w.failSince.UTC().Format(time.RFC3339)))
	w.failing, w.failErr, w.failSent = false, "", time.Time{}
}

// check measures the volume holding the archive and warns when it is running out.
func (w *storageWatch) check() {
	free, total, err := w.free(w.path)
	if err != nil || total == 0 {
		return
	}
	frac := float64(free) / float64(total)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.freeFrac = frac
	if frac >= storageLowFraction && free >= storageLowBytes {
		w.lowSent = time.Time{}
		return
	}
	now := w.now()
	if now.Sub(w.lowSent) < storageLowRepeat {
		return
	}
	w.lowSent = now
	msg := fmt.Sprintf("ego's session volume is running out: %d MiB free of %d MiB (%.0f%%).", free>>20, total>>20, frac*100)
	logx.Warnf("%s", msg)
	w.send(msg)
}

// health is what /api/health says about the archive: "ok", "low" (volume running out) or "failing" (writes fail).
func (w *storageWatch) health() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := "ok"
	switch {
	case w.failing:
		status = "failing"
	case w.freeFrac >= 0 && w.freeFrac < storageLowFraction:
		status = "low"
	}
	out := map[string]any{"status": status}
	if w.freeFrac >= 0 {
		out["freePercent"] = int(w.freeFrac*100 + 0.5)
	}
	if w.failing {
		out["failingSince"] = w.failSince.UTC().Format(time.RFC3339)
		out["error"] = w.failErr
	}
	return out
}
