package online

import (
	"math"
	"sort"
	"time"
)

// Sample is one stored score of a series.
type Sample struct {
	SessionID string
	// Score is the score of record: a person's when the session was labelled with one, else the judge's.
	Score   float64
	Version string
	Model   string
	At      time.Time
	// Human is true when a person confirmed or gave the score; Note is what they wrote.
	Human bool
	Note  string
}

// SeriesKey identifies one (tenant, kind, metric) series.
type SeriesKey struct {
	Tenant string `json:"tenant"`
	Kind   string `json:"kind"`
	Metric string `json:"metric"`
}

// TestResult compares a series' current window with its baseline.
type TestResult struct {
	BaseMean float64 `json:"baseMean"`
	CurMean  float64 `json:"curMean"`
	NBase    int     `json:"nBase"`
	NCur     int     `json:"nCur"`
	Drop     float64 `json:"drop"`
	Z        float64 `json:"z"`
	// Enough is true when both sides have minSamples.
	Enough    bool `json:"enough"`
	Regressed bool `json:"regressed"`
	// Suspect names a version or model that has at least half of the
	// current samples and under a tenth of the baseline.
	Suspect string `json:"suspect,omitempty"`
	// Worst lists up to three current sessions with the lowest scores.
	Worst []string `json:"worst,omitempty"`
}

// zThreshold is how many standard errors the drop must exceed.
const zThreshold = 2.5

func meanVar(xs []Sample) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var m float64
	for _, x := range xs {
		m += x.Score
	}
	m /= float64(len(xs))
	if len(xs) < 2 {
		return m, 0
	}
	var v float64
	for _, x := range xs {
		v += (x.Score - m) * (x.Score - m)
	}
	return m, v / float64(len(xs)-1)
}

// Test runs the regression test on one series.
func Test(base, cur []Sample, cfg Detector) TestResult {
	r := TestResult{NBase: len(base), NCur: len(cur)}
	var vb, vc float64
	r.BaseMean, vb = meanVar(base)
	r.CurMean, vc = meanVar(cur)
	r.Drop = r.BaseMean - r.CurMean
	r.Enough = r.NBase >= cfg.MinSamples && r.NCur >= cfg.MinSamples
	if r.NBase > 0 && r.NCur > 0 {
		se := math.Sqrt(vb/float64(r.NBase) + vc/float64(r.NCur))
		// A floor keeps an all-identical baseline (variance 0) from making
		// any tiny drop infinitely significant.
		se = math.Max(se, 0.01)
		r.Z = r.Drop / se
	}
	r.Regressed = r.Enough && r.Drop >= cfg.MinDrop && r.Z > zThreshold
	if !r.Regressed {
		return r
	}
	r.Suspect = suspect(base, cur)
	worst := append([]Sample(nil), cur...)
	sort.SliceStable(worst, func(i, j int) bool { return worst[i].Score < worst[j].Score })
	seen := map[string]bool{}
	for _, w := range worst {
		if len(r.Worst) == 3 {
			break
		}
		if !seen[w.SessionID] {
			seen[w.SessionID] = true
			r.Worst = append(r.Worst, w.SessionID)
		}
	}
	return r
}

func suspect(base, cur []Sample) string {
	share := func(xs []Sample, f func(Sample) string) map[string]float64 {
		m := map[string]float64{}
		for _, x := range xs {
			if v := f(x); v != "" {
				m[v] += 1 / float64(len(xs))
			}
		}
		return m
	}
	for _, dim := range []struct {
		label string
		get   func(Sample) string
	}{
		{"version", func(s Sample) string { return s.Version }},
		{"model", func(s Sample) string { return s.Model }},
	} {
		cs, bs := share(cur, dim.get), share(base, dim.get)
		best, bestShare := "", 0.0
		for v, sh := range cs {
			if sh >= 0.5 && bs[v] < 0.1 && (sh > bestShare || (sh == bestShare && v < best)) {
				best, bestShare = v, sh
			}
		}
		if best != "" {
			return dim.label + " " + best
		}
	}
	return ""
}

// Alert events.
const (
	EventOpen     = "open"
	EventUpdated  = "updated"
	EventResolved = "resolved"
)

// Alert is the lifecycle state of one series' regression.
type Alert struct {
	ID         string     `json:"id"`
	Key        SeriesKey  `json:"key"`
	Status     string     `json:"status"` // open | resolved
	OpenedAt   time.Time  `json:"openedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	// Last is the latest test result; NotifiedDrop the drop of the last
	// open or update notification.
	Last         TestResult `json:"last"`
	NotifiedDrop float64    `json:"notifiedDrop"`
	QuietChecks  int        `json:"quietChecks"`
}

// resolveAfter is how many quiet checks in a row resolve an open alert.
const resolveAfter = 2

// Step advances one series' alert with a new test result and returns the
// next state and the event to notify ("" for none). prev may be nil or
// resolved; newID names a newly opened alert.
func Step(prev *Alert, key SeriesKey, r TestResult, cfg Detector, now time.Time, newID func() string) (*Alert, string) {
	if prev == nil || prev.Status != "open" {
		if !r.Regressed {
			return prev, ""
		}
		return &Alert{ID: newID(), Key: key, Status: "open", OpenedAt: now, UpdatedAt: now, Last: r, NotifiedDrop: r.Drop}, EventOpen
	}
	a := *prev
	a.Last = r
	switch {
	case r.Regressed && r.Drop >= a.NotifiedDrop+cfg.MinDrop:
		a.QuietChecks = 0
		a.NotifiedDrop = r.Drop
		a.UpdatedAt = now
		return &a, EventUpdated
	case r.NCur == 0 || (r.NCur >= cfg.MinSamples && r.Drop < cfg.MinDrop/2):
		// Quiet: recovered, or the series stopped (no current samples).
		a.QuietChecks++
		if a.QuietChecks >= resolveAfter {
			a.Status = "resolved"
			a.ResolvedAt = &now
			a.UpdatedAt = now
			return &a, EventResolved
		}
		return &a, ""
	default:
		a.QuietChecks = 0
		return &a, ""
	}
}
