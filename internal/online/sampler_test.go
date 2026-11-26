package online

import (
	"strconv"
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

// The daily cap counts both lanes, survives a restart through Seed, and starts again the next UTC day.
func TestSampler_DailyCap(t *testing.T) {
	s := NewSampler(Sampling{BoostPerKindPerHour: 100, DedupWindow: Duration(time.Hour)})
	now := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	s.Seed("a", "reflection", 1, now)
	sampled := 0
	for i := 0; i < 6; i++ {
		f := Features{Tenant: "a", Kind: "reflection", Errors: 1, Fingerprint: strconv.Itoa(i)}
		d := s.DecideCapped(f, 100, 3, now.Add(time.Duration(i)*time.Minute))
		if d.Sample {
			sampled++
		} else if d.Skip != SkipDaily {
			t.Errorf("skip = %q, want %q", d.Skip, SkipDaily)
		}
	}
	if sampled != 2 {
		t.Errorf("sampled %d with one already counted and a cap of 3, want 2", sampled)
	}
	if d := s.DecideCapped(Features{Tenant: "a", Kind: "reflection", Fingerprint: "next"}, 100, 3, now.Add(25*time.Hour)); !d.Sample {
		t.Errorf("the next day: skip %q, want a sample", d.Skip)
	}
}
