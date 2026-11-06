package api

import (
	"sync"
	"time"
)

// failWindow counts the refusals of one source in the current minute.
type failWindow struct {
	start time.Time
	n     int
}

const (
	// failLimit refusals in one failWindowLen from one source make the next ones answer "slow down" instead of
	// "unauthorized"; failTracked bounds the table of sources.
	failLimit     = 30
	failWindowLen = time.Minute
	failTracked   = 4096
)

// failCounter counts the refused credentials of each source address, so one source cannot keep the server busy
// checking values that are not credentials. The zero value is ready to use.
type failCounter struct {
	mu sync.Mutex
	m  map[string]failWindow
}

// record counts a refusal from source and reports that the source is over its limit.
func (c *failCounter) record(source string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.m == nil {
		c.m = map[string]failWindow{}
	}
	w := c.m[source]
	if now.Sub(w.start) > failWindowLen {
		w = failWindow{start: now}
	}
	w.n++
	if len(c.m) >= failTracked {
		for k, v := range c.m {
			if now.Sub(v.start) > failWindowLen {
				delete(c.m, k)
			}
		}
		if len(c.m) >= failTracked {
			clear(c.m)
		}
	}
	c.m[source] = w
	return w.n > failLimit
}

// over reports whether source has used up its refusals in the current window, without counting another.
func (c *failCounter) over(source string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.m[source]
	return ok && time.Since(w.start) <= failWindowLen && w.n > failLimit
}
