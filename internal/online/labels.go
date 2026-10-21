package online

import (
	"math"
	"sort"
	"time"
)

// Labels are a person's verdict on a judge's score (docs/rsi.md, section 4). This file holds the decisions only:
// how agreement is counted, when a metric is too uncalibrated to drive findings, and which labelled sessions are
// re-scored next. internal/api reads the stores and runs the judge.

// Label verdicts.
const (
	VerdictAgree    = "agree"
	VerdictDisagree = "disagree"
)

// Label limits and the numbers of the calibration gate.
const (
	// AgreementWindow is how far back labels count towards a metric's agreement.
	AgreementWindow = 7 * 24 * time.Hour
	// GateMinLabels is how many labels a metric needs in the window before low agreement stops its findings: a
	// handful of disagreements is not evidence that a judge is broken.
	GateMinLabels = 10
	// AnchorsPerMetric bounds one re-scoring pass, which spends judge budget.
	AnchorsPerMetric  = 20
	MaxLabelNoteChars = 1000
	MaxLabelByChars   = 200
)

// Label is one verdict on one metric of one session. The wire shape is snake_case like findings and corrections.
type Label struct {
	ID        string `json:"label_id"`
	SessionID string `json:"session_id"`
	Tenant    string `json:"tenant"`
	Kind      string `json:"kind"`
	Metric    string `json:"metric"`
	// JudgeScore is the online score at the moment of labelling; the judge may later be re-scored.
	JudgeScore float64 `json:"judge_score"`
	Verdict    string  `json:"verdict"`
	// Score is the score the person says is right; absent when they only agreed or disagreed.
	Score     *float64  `json:"score,omitempty"`
	Note      string    `json:"note,omitempty"`
	By        string    `json:"by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Reference is the score a re-scored judge is compared with: the person's own score, else what the judge said when
// it was labelled.
func (l Label) Reference() float64 {
	if l.Score != nil {
		return *l.Score
	}
	return l.JudgeScore
}

// ValidVerdict reports whether v is agree or disagree.
func ValidVerdict(v string) bool { return v == VerdictAgree || v == VerdictDisagree }

type labelKey struct{ session, metric string }

// Latest keeps the newest label of each (session, metric), so a person who changes their mind, or an agent that
// posts the same label twice, counts once. Order is by creation, oldest first.
func Latest(labels []Label) []Label {
	newest := map[labelKey]Label{}
	for _, l := range labels {
		k := labelKey{l.SessionID, l.Metric}
		if cur, ok := newest[k]; !ok || l.CreatedAt.After(cur.CreatedAt) || (l.CreatedAt.Equal(cur.CreatedAt) && l.ID > cur.ID) {
			newest[k] = l
		}
	}
	out := make([]Label, 0, len(newest))
	for _, l := range newest {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Agreement counts the verdicts on one metric.
type Agreement struct {
	Agree    int
	Disagree int
}

// Total is how many labels were counted.
func (a Agreement) Total() int { return a.Agree + a.Disagree }

// Rate is agree / (agree + disagree); ok is false with no labels.
func (a Agreement) Rate() (rate float64, ok bool) {
	if a.Total() == 0 {
		return 0, false
	}
	return float64(a.Agree) / float64(a.Total()), true
}

// Gated reports whether the metric's judge is too far from the people who checked it to drive findings: enough
// labels, and agreement below the configured floor.
func (a Agreement) Gated(minAgreement float64) bool {
	rate, ok := a.Rate()
	return ok && a.Total() >= GateMinLabels && rate < minAgreement
}

// AgreementByMetric counts the latest label of each session and metric, per metric.
func AgreementByMetric(labels []Label) map[string]Agreement {
	out := map[string]Agreement{}
	for _, l := range Latest(labels) {
		a := out[l.Metric]
		switch l.Verdict {
		case VerdictAgree:
			a.Agree++
		case VerdictDisagree:
			a.Disagree++
		}
		out[l.Metric] = a
	}
	return out
}

// AnchorKey names one labelled (session, metric) in the store that remembers when it was last re-scored.
func AnchorKey(sessionID, metric string) string { return metric + "\x00" + sessionID }

// PickAnchors chooses up to n labelled sessions of one metric to re-score: the ones re-scored longest ago first
// (never re-scored counts as longest), then the oldest label, then the smaller session id, so a pass is
// reproducible and every anchor gets its turn. rescored maps AnchorKey to the last re-score time.
func PickAnchors(labels []Label, rescored map[string]time.Time, n int) []Label {
	anchors := Latest(labels)
	sort.SliceStable(anchors, func(i, j int) bool {
		a, b := anchors[i], anchors[j]
		ra, rb := rescored[AnchorKey(a.SessionID, a.Metric)], rescored[AnchorKey(b.SessionID, b.Metric)]
		if !ra.Equal(rb) {
			return ra.Before(rb)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.SessionID < b.SessionID
	})
	if len(anchors) > n {
		anchors = anchors[:n]
	}
	return anchors
}

// MeanAbsDiff is the mean of |a[i] - b[i]|; zero for no pairs.
func MeanAbsDiff(a, b []float64) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += math.Abs(a[i] - b[i])
	}
	return sum / float64(n)
}

// Drift is how far a metric's judge has moved from its labelled anchors, measured at one re-scoring pass.
type Drift struct {
	// MeanAbsDiff is the mean absolute difference between the new score and the label's reference score.
	MeanAbsDiff float64 `json:"mean_abs_diff"`
	// N is how many anchors were re-scored in the pass.
	N  int       `json:"n"`
	At time.Time `json:"at"`
}

// AgreementRow is one metric's line in the daily digest.
type AgreementRow struct {
	Metric string
	Agreement
	Gated bool
}
