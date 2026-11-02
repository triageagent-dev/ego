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
	// DefaultArchiveAlertPercent is the free space, in percent of the volume, under which it is "running out".
	DefaultArchiveAlertPercent = 10

	// storageLowBytes: free space below this is "running out" on a volume of any size.
	storageLowBytes = 256 << 20

	// A condition that lasts is repeated, not announced once and forgotten.
	storageFailRepeat = time.Hour
	storageLowRepeat  = 6 * time.Hour
)

type storageWatch struct {
	path   string
	notice func(text string)
	now    func() time.Time
	free   func(path string) (freeBytes, totalBytes uint64, err error)
	// lowFraction is the share of the volume that must stay free.
	lowFraction float64

	mu        sync.Mutex
	failing   bool
	failSince time.Time
	failSent  time.Time
	lowSent   time.Time
	freeFrac  float64 // -1 until measured
}

func newStorageWatch(path string, alertPercent float64) *storageWatch {
	if alertPercent <= 0 {
		alertPercent = DefaultArchiveAlertPercent
	}
	return &storageWatch{path: path, now: time.Now, free: volumeFree, freeFrac: -1, lowFraction: alertPercent / 100}
}

func (w *storageWatch) send(text string) {
	if w.notice != nil {
		w.notice(text)
	}
}

// writeFailed records a failed archive write and says so on entering the state and every storageFailRepeat after.
//
// The notice is sent after the lock is released: it is an HTTP post, and /api/health takes the same lock, so a slow
// sink must not stall the readiness probe.
func (w *storageWatch) writeFailed(err error) {
	w.mu.Lock()
	now := w.now()
	if !w.failing {
		w.failing, w.failSince = true, now
	}
	if now.Sub(w.failSent) < storageFailRepeat {
		w.mu.Unlock()
		return
	}
	w.failSent = now
	text := fmt.Sprintf("ego cannot write its session archive (since %s): %v. Sessions are held in memory only and are lost on a restart.",
		w.failSince.UTC().Format(time.RFC3339), err)
	w.mu.Unlock()
	w.send(text)
}

func (w *storageWatch) writeOK() {
	w.mu.Lock()
	if !w.failing {
		w.mu.Unlock()
		return
	}
	text := fmt.Sprintf("ego writes its session archive again (it failed since %s).", w.failSince.UTC().Format(time.RFC3339))
	w.failing, w.failSent = false, time.Time{}
	w.mu.Unlock()
	w.send(text)
}

// check measures the volume holding the archive and warns when it is running out.
func (w *storageWatch) check() {
	free, total, err := w.free(w.path)
	if err != nil || total == 0 {
		return
	}
	frac := float64(free) / float64(total)
	w.mu.Lock()
	w.freeFrac = frac
	if frac >= w.lowFraction && free >= storageLowBytes {
		w.lowSent = time.Time{}
		w.mu.Unlock()
		return
	}
	now := w.now()
	if now.Sub(w.lowSent) < storageLowRepeat {
		w.mu.Unlock()
		return
	}
	w.lowSent = now
	w.mu.Unlock()
	msg := fmt.Sprintf("ego's session volume is running out: %d MiB free of %d MiB (%.0f%%).", free>>20, total>>20, frac*100)
	logx.Warnf("%s", msg)
	w.send(msg)
}

// health is what /api/health says about the archive: "ok", "low" (volume running out) or "failing" (writes fail).
// The endpoint needs no login, so the failed write's error text (a path and a database message) is kept for the
// log and the notice, not the answer.
func (w *storageWatch) health() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := "ok"
	switch {
	case w.failing:
		status = "failing"
	case w.freeFrac >= 0 && w.freeFrac < w.lowFraction:
		status = "low"
	}
	out := map[string]any{"status": status}
	if w.freeFrac >= 0 {
		out["freePercent"] = int(w.freeFrac*100 + 0.5)
	}
	if w.failing {
		out["failingSince"] = w.failSince.UTC().Format(time.RFC3339)
	}
	return out
}
