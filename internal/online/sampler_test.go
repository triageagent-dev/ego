package online

import (
	"testing"
	"time"
)

// A rate below one an hour samples at that rate; it does not sample nothing.
func TestBucket_RateBelowOneAnHour(t *testing.T) {
	var b bucket
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	taken := 0
	for h := 0; h <= 48; h++ {
		if b.take(0.25, start.Add(time.Duration(h)*time.Hour)) {
			taken++
		}
	}
	if taken != 13 {
		t.Errorf("0.25 an hour over 48 hours took %d, want 13 (the first, then one every four hours)", taken)
	}
	var none bucket
	if none.take(0, start) {
		t.Error("a rate of 0 took a token")
	}
}
