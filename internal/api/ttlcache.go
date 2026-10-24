package api

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// A short cache and a concurrency gate for the reads that rebuild many sessions (Activity, Turns): any signed-in
// viewer can call them, and each call walks the archive under the store lock. An answer is kept per caller role while
// nothing in the store has changed (the key carries the store's change counter), for a few seconds at most, and only
// a few run at once.

const (
	readCacheTTL     = 5 * time.Second
	readCacheEntries = 256
	readConcurrency  = 2
)

type cachedRead struct {
	at   time.Time
	body []byte
}

// readCache keeps the encoded answers of one endpoint.
type readCache struct {
	mu   sync.Mutex
	m    map[string]cachedRead
	gate chan struct{}
	now  func() time.Time
}

func newReadCache() *readCache {
	return &readCache{m: map[string]cachedRead{}, gate: make(chan struct{}, readConcurrency), now: time.Now}
}

func (c *readCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || c.now().Sub(e.at) > readCacheTTL {
		return nil, false
	}
	return e.body, true
}

func (c *readCache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= readCacheEntries {
		for k, e := range c.m {
			if c.now().Sub(e.at) > readCacheTTL {
				delete(c.m, k)
			}
		}
		if len(c.m) >= readCacheEntries {
			clear(c.m)
		}
	}
	c.m[key] = cachedRead{at: c.now(), body: body}
}

// acquire takes one of the few places to compute an answer; it is false when all are taken.
func (c *readCache) acquire() bool {
	select {
	case c.gate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *readCache) release() { <-c.gate }

// roleKey names what a role may see, so an answer is shared only between callers who see the same.
func roleKey(info RoleInfo) string {
	agents := append([]string(nil), info.Agents...)
	tenants := append([]string(nil), info.Tenants...)
	sort.Strings(agents)
	sort.Strings(tenants)
	return fmt.Sprintf("%s|%v|%v|%v|%s|%s", info.Role, info.IsAdmin, info.Unresolved, info.Agents == nil, strings.Join(agents, ","), strings.Join(tenants, ","))
}
