package api

import "testing"

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
