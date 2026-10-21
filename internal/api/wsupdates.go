package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/triageagent-dev/ego/internal/logx"
)

var wsUpgrader = websocket.Upgrader{
	// Cross-origin upgrades are refused by originGuard, which wraps every route on the main listener; a
	// same-origin check here would only duplicate it and could reject requests behind a gateway that rewrites Host.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsPingInterval is how often the server pings and sends the heartbeat; a variable so a test can shorten it.
var wsPingInterval = 15 * time.Second

const (
	wsWriteTimeout = 10 * time.Second
	wsPongTimeout  = 60 * time.Second
)

// heartbeatEvent is sent every wsPingInterval, whatever else the feed carries.
type heartbeatEvent struct {
	Type string `json:"type"`
}

// wsUpdatesHandler implements GET /ws/ui-updates: a WebSocket alternative to /stream/ui-updates (SSE), added
// because the cluster's gateway (agentgateway) w. Both endpoints draw from the same sseHub, so an event
// broadcasts once and reaches SSE and WS clients alike - this is an addition, not a replacement;
// direct/port-forward access still works over SSE exactly as before.
func wsUpdatesHandler(hub *sseHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			logx.Debugf("ws-updates: upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		ch := hub.registerFor(roleInfoOrOpen(r.Context()))
		defer hub.unregister(ch)

		// Read pump: WS requires draining incoming frames (pong/close
		// control frames arrive on the read side even though this client
		// never sends application messages) to detect disconnection and
		// keep the connection alive. closed signals the write loop to stop
		// once the read side errors out (client closed, network drop, ...).
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			_ = conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
			conn.SetPongHandler(func(string) error {
				return conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
			})
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := conn.WriteJSON(event); err != nil {
					return
				}
				if hub.takeResync(ch) {
					if err := conn.WriteJSON(resyncEvent{Type: "resync"}); err != nil {
						return
					}
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
				// A ping frame is invisible to the browser's script. This message is what tells the UI the link is
				// alive, so it can show the connection state from the socket alone instead of polling the server.
				if err := conn.WriteJSON(heartbeatEvent{Type: "heartbeat"}); err != nil {
					return
				}
				// A client that overflowed and then went quiet still learns
				// it missed events.
				if hub.takeResync(ch) {
					if err := conn.WriteJSON(resyncEvent{Type: "resync"}); err != nil {
						return
					}
				}
			case <-closed:
				return
			case <-r.Context().Done():
				return
			}
		}
	}
}
