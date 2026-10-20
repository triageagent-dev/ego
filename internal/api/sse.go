package api

import (
	"encoding/json"
	"net/http"
	"sync"
)

// sseHub fans out events to every connected /stream/ui-updates client.
type sseHub struct {
	mu sync.Mutex
	// clients maps each client's channel to whether an event was dropped
	// for it since it was last told to resync.
	clients map[chan any]bool
}

// hubClientBuffer is each client's event backlog. A traced server exports
// spans in batches of hundreds, so this has to absorb a burst of
// session_started/session_complete/update events between writes.
const hubClientBuffer = 1024

// resyncEvent tells a client that events were dropped for it, so it must re-read the session list rather than
// trust its incremental state.
type resyncEvent struct {
	Type string `json:"type"`
}

func newSSEHub() *sseHub {
	return &sseHub{clients: map[chan any]bool{}}
}

func (h *sseHub) register() chan any {
	ch := make(chan any, hubClientBuffer)
	h.mu.Lock()
	h.clients[ch] = false
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unregister(ch chan any) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// broadcast sends event to every connected client. A client whose buffer is full is skipped rather than
// blocking the ingest path, since a slow UI client should never stall trace ingestion; it is marked for a
// resync instead (takeResync).
func (h *sseHub) broadcast(event any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- event:
		default:
			h.clients[ch] = true
		}
	}
}

// takeResync reports whether events were dropped for ch since the last
// call, clearing the mark. Writers check it after each write and send a
// resyncEvent when it is set.
func (h *sseHub) takeResync(ch chan any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	lagged := h.clients[ch]
	if lagged {
		h.clients[ch] = false
	}
	return lagged
}

// uiUpdatesHandler implements GET /stream/ui-updates: a standard text/event-stream SSE endpoint the UI
// subscribes to via EventSource.
func uiUpdatesHandler(hub *sseHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch := hub.register()
		defer hub.unregister(ch)

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return
				}
				if err := writeSSE(w, event); err != nil {
					return
				}
				if hub.takeResync(ch) {
					if err := writeSSE(w, resyncEvent{Type: "resync"}); err != nil {
						return
					}
				}
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
