package online

import "time"

// Corrections record what an agent (or a person) changed in answer to a finding, and ego's verdict on whether the
// scores recovered (docs/rsi.md, section 3).

// Correction states.
const (
	CorrectionPending  = "pending"
	CorrectionVerified = "verified"
	CorrectionFailed   = "failed"
)

// Limits on a registered correction.
const (
	MaxWhatChars = 2000
	MaxByChars   = 200
	MaxVersion   = 200
)

// VWindow is one side of a verification: the mean score and the number of sessions.
type VWindow struct {
	Mean float64 `json:"mean"`
	N    int     `json:"n"`
}

// Verification is the numbers behind a correction's verdict. Before is fixed when the correction is registered;
// After is refreshed on every detector tick until the verdict.
type Verification struct {
	Before VWindow `json:"before"`
	After  VWindow `json:"after"`
	// WindowEnd is when a correction that has not shown enough sessions counts as failed.
	WindowEnd time.Time `json:"window_end"`
	// Reason says in a few words why the verdict is what it is.
	Reason string `json:"reason,omitempty"`
}

// CorrectionData is the JSON in online_corrections.data.
type CorrectionData struct {
	What         string       `json:"what"`
	By           string       `json:"by,omitempty"`
	Verification Verification `json:"verification"`
}

// Correction is one registered change. The wire shape is the MCP tools' snake_case.
type Correction struct {
	ID         string     `json:"correction_id"`
	FindingID  string     `json:"finding_id,omitempty"`
	Tenant     string     `json:"tenant"`
	Kind       string     `json:"kind"`
	Metric     string     `json:"metric,omitempty"`
	Version    string     `json:"version,omitempty"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	CorrectionData
}

// Note is the correction as a finding's history keeps it.
func (c *Correction) Note() CorrectionNote {
	v := c.Verification
	return CorrectionNote{CorrectionID: c.ID, What: c.What, Version: c.Version, By: c.By, State: c.State, At: c.CreatedAt, Verification: &v}
}

// improvementEpsilon absorbs float error in "improved by at least minDrop / 2".
const improvementEpsilon = 1e-9

// MeanOf is the mean score of samples and their count.
func MeanOf(samples []Sample) VWindow {
	if len(samples) == 0 {
		return VWindow{}
	}
	var sum float64
	for _, s := range samples {
		sum += s.Score
	}
	return VWindow{Mean: sum / float64(len(samples)), N: len(samples)}
}

// VerifyInput is one pending correction at one tick.
type VerifyInput struct {
	Before VWindow
	// After are the scores since the correction (at its version, when it names one).
	After []Sample
	// AlertResolved is true when the alert the finding came from has resolved.
	AlertResolved bool
	Created, Now  time.Time
}

// Verify decides a pending correction. With at least det.MinSamples sessions after it: verified when the mean
// improved by det.MinDrop/2 or the alert resolved, failed otherwise. With fewer it stays pending until
// f.VerifyWithin has elapsed, then fails. A correction with no baseline (nothing scored before it) is compared with
// f.MinScore instead, the only bar left.
func Verify(in VerifyInput, det Detector, f Findings) (state string, v Verification) {
	after := MeanOf(in.After)
	v = Verification{Before: in.Before, After: after, WindowEnd: in.Created.Add(f.VerifyWithin.D())}
	var improved bool
	if in.Before.N == 0 {
		improved = after.N > 0 && after.Mean >= f.MinScore
	} else {
		improved = after.Mean-in.Before.Mean >= det.MinDrop/2-improvementEpsilon
	}
	switch {
	case after.N >= det.MinSamples && (improved || in.AlertResolved):
		v.Reason = "the mean improved"
		if !improved {
			v.Reason = "the alert resolved"
		}
		return CorrectionVerified, v
	case after.N >= det.MinSamples:
		v.Reason = "the mean did not improve enough"
		return CorrectionFailed, v
	case !in.Now.Before(v.WindowEnd):
		v.Reason = "too few sessions were scored before the window ended"
		return CorrectionFailed, v
	}
	return CorrectionPending, v
}
