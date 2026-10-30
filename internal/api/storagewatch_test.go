package api

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testWatch(free, total uint64) (*storageWatch, *[]string, *time.Time) {
	var sent []string
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	w := newStorageWatch("/data/archive.db", 0)
	w.now = func() time.Time { return now }
	w.free = func(string) (uint64, uint64, error) { return free, total, nil }
	w.notice = func(text string) { sent = append(sent, text) }
	return w, &sent, &now
}

func TestStorageWatch_FailedWriteIsAnnouncedOnceThenRepeatedAndRecoveryIsSaid(t *testing.T) {
	w, sent, now := testWatch(5<<30, 10<<30)

	w.writeFailed(errors.New("database or disk is full"))
	w.writeFailed(errors.New("database or disk is full"))
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "database or disk is full") {
		t.Fatalf("notices after two failures in a row = %q, want one naming the error", *sent)
	}
	if got := w.health()["status"]; got != "failing" {
		t.Errorf("health status = %v, want failing", got)
	}

	*now = now.Add(storageFailRepeat + time.Minute)
	w.writeFailed(errors.New("database or disk is full"))
	if len(*sent) != 2 {
		t.Errorf("a failure that lasts was not repeated: %q", *sent)
	}

	w.writeOK()
	if len(*sent) != 3 || !strings.Contains((*sent)[2], "again") {
		t.Errorf("recovery not said: %q", *sent)
	}
	w.writeOK()
	if len(*sent) != 3 {
		t.Errorf("a healthy write said something: %q", *sent)
	}
	if got := w.health()["status"]; got != "ok" {
		t.Errorf("health status after recovery = %v, want ok", got)
	}
}

func TestStorageWatch_LowVolumeWarnsOncePerRepeatWindow(t *testing.T) {
	w, sent, now := testWatch(40<<20, 1<<30)
	w.check()
	w.check()
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "running out") {
		t.Fatalf("notices = %q, want one low-volume warning", *sent)
	}
	if got := w.health()["status"]; got != "low" {
		t.Errorf("health status = %v, want low", got)
	}
	*now = now.Add(storageLowRepeat + time.Minute)
	w.check()
	if len(*sent) != 2 {
		t.Errorf("a volume that stays low was not repeated: %q", *sent)
	}
}

func TestStorageWatch_RoomyVolumeSaysNothing(t *testing.T) {
	w, sent, _ := testWatch(5<<30, 10<<30)
	w.check()
	if len(*sent) != 0 || w.health()["status"] != "ok" || w.health()["freePercent"] != 50 {
		t.Errorf("notices %q, health %v", *sent, w.health())
	}
}

func TestStorageWatch_NoNoticeSinkStillTracksHealth(t *testing.T) {
	w := newStorageWatch("/data/archive.db", 0)
	w.writeFailed(errors.New("disk full"))
	if w.health()["status"] != "failing" {
		t.Error("a failure without a sink is not tracked")
	}
}

// The threshold is a parameter, so the warning can be seen working on a volume that is not nearly full.
func TestStorageWatch_AlertPercentIsAParameter(t *testing.T) {
	w, sent, _ := testWatch(60<<30, 100<<30)
	w.check()
	if len(*sent) != 0 {
		t.Fatalf("60%% free warned at the default threshold: %q", *sent)
	}
	w.lowFraction = 0.70
	w.check()
	if len(*sent) != 1 || w.health()["status"] != "low" {
		t.Errorf("60%% free did not warn at a 70%% threshold: %q, %v", *sent, w.health())
	}
}
