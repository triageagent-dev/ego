package api

import (
	"encoding/json"
	"net/http"
	"sync"
)

// sseHub fans out events to every connected /stream/ui-updates client.
type sseHub struct {
	mu sync.Mutex
	// clients maps each client's channel to its state.
	clients map[chan any]*hubClient
}

// hubClient is one subscriber. A client with a scoped role only receives events of sessions whose agents are in
// its scope; seen remembers which sessions it has been told about so a session that becomes visible later (its
// agent name arrives in a later span) makes it re-read the list instead of receiving updates for a session it
// never heard of.
type hubClient struct {
	// lagged is set when an event was dropped for the client since it was last told to resync.
	lagged bool
	scoped bool
	info   RoleInfo
	seen   map[string]bool
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
	return &sseHub{clients: map[chan any]*hubClient{}}
}

// register subscribes a client that receives every event.
func (h *sseHub) register() chan any {
	return h.add(&hubClient{})
}

// registerFor subscribes a client limited by info: a scoped role only receives events of sessions whose agents
// it may see.
func (h *sseHub) registerFor(info RoleInfo) chan any {
	if !info.Scoped() && !info.TenantScoped() {
		return h.register()
	}
	return h.add(&hubClient{scoped: true, info: info, seen: map[string]bool{}})
}

func (h *sseHub) add(c *hubClient) chan any {
	ch := make(chan any, hubClientBuffer)
	h.mu.Lock()
	h.clients[ch] = c
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unregister(ch chan any) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// broadcast sends event to every unscoped client. A client whose buffer is full is skipped rather than
// blocking the ingest path, since a slow UI client should never stall trace ingestion; it is marked for a
// resync instead (takeResync).
func (h *sseHub) broadcast(event any) {
	h.broadcastSession(event, "", nil, nil, false)
}

// broadcastSession sends an event about sessionID, whose agents and tenants are given, to every client allowed to see
// it. started marks the session_started event, the only one a scoped client may meet before it has heard of
// the session; any other event for a session it has not seen yet turns into a resync, so the client reads the
// list (filtered the same way) instead of building on a session it never received.
func (h *sseHub) broadcastSession(event any, sessionID string, agents, tenants []string, started bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, c := range h.clients {
		if c.scoped {
			if sessionID == "" || !c.info.AllowsSession(agents, tenants) {
				continue
			}
			if !c.seen[sessionID] {
				c.seen[sessionID] = true
				if !started {
					c.lagged = true
					continue
				}
			}
		}
		select {
		case ch <- event:
		default:
			c.lagged = true
		}
	}
}

// takeResync reports whether events were dropped for ch since the last
// call, clearing the mark. Writers check it after each write and send a
// resyncEvent when it is set.
func (h *sseHub) takeResync(ch chan any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.clients[ch]
	if c == nil || !c.lagged {
		return false
	}
	c.lagged = false
	return true
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

		ch := hub.registerFor(roleInfoOrOpen(r.Context()))
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

// hasScoped reports whether any connected client is limited to a set of agents, so the ingest path only pays
// for reading a session's agents when someone needs them.
func (h *sseHub) hasScoped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		if c.scoped {
			return true
		}
	}
	return false
}
