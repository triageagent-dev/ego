package api

import (
	"path/filepath"
	"testing"
	"time"
)

// A client whose buffer overflows is marked for a resync exactly once, and
// a client that kept up is not.
func TestSSEHub_OverflowMarksResync(t *testing.T) {
	hub := newSSEHub()
	slow := hub.register()
	fast := hub.register()
	defer hub.unregister(slow)
	defer hub.unregister(fast)

	for i := 0; i < hubClientBuffer+5; i++ {
		hub.broadcast(i)
		if i < hubClientBuffer {
			<-fast
		}
		if i >= hubClientBuffer-1 {
			// keep fast drained past the point slow overflows
			select {
			case <-fast:
			default:
			}
		}
	}
	if !hub.takeResync(slow) {
		t.Error("overflowed client not marked for resync")
	}
	if hub.takeResync(slow) {
		t.Error("resync mark not cleared after takeResync")
	}
	if hub.takeResync(fast) {
		t.Error("client that kept up marked for resync")
	}
}

// A burst of writes reaches every client, scoped ones included, as one "changed" message per topic.
func TestSSEHub_NotifyChangedDebouncesPerTopic(t *testing.T) {
	old := changeDebounce
	changeDebounce = 20 * time.Millisecond
	defer func() { changeDebounce = old }()

	hub := newSSEHub()
	open := hub.register()
	scoped := hub.registerFor(scopedTo("a"))
	defer hub.unregister(open)
	defer hub.unregister(scoped)

	for i := 0; i < 50; i++ {
		hub.notifyChanged(topicUsage)
	}
	hub.notifyChanged(topicRuns)

	for name, ch := range map[string]chan any{"open": open, "scoped": scoped} {
		got := map[string]int{}
		deadline := time.After(time.Second)
		for len(got) < 2 {
			select {
			case ev := <-ch:
				e, ok := ev.(changedEvent)
				if !ok || e.Type != "changed" {
					t.Fatalf("%s: unexpected event %#v", name, ev)
				}
				got[e.Topic]++
			case <-deadline:
				t.Fatalf("%s: got %v, want one message each for usage and runs", name, got)
			}
		}
		time.Sleep(60 * time.Millisecond)
		select {
		case ev := <-ch:
			t.Errorf("%s: a second message for the same burst: %#v", name, ev)
		default:
		}
		if got[topicUsage] != 1 || got[topicRuns] != 1 {
			t.Errorf("%s: got %v, want exactly one each", name, got)
		}
	}
}

// The archive reports a finished write, not a failed one.
func TestSQLiteStore_AnnouncesWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "announce.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var topics []string
	store.SetOnChange(func(topic string) { topics = append(topics, topic) })

	if err := store.InsertUsage([]usageRow{{SpanID: "s1", TraceID: "t1", SessionID: "x", Kind: "k"}}); err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0] != topicUsage {
		t.Fatalf("topics after a usage write = %v, want [usage]", topics)
	}
	if err := store.InsertUsage(nil); err != nil || len(topics) != 1 {
		t.Fatalf("an empty write changed nothing, topics = %v err = %v", topics, err)
	}
}
