package api

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/online"
)

// A person's label is the score of record: their score replaces the judge's in every aggregate read, an
// agreement keeps the judge's score and marks it confirmed, and a disagreement without a score leaves the
// session out. Only the newest label of a (session, metric) counts.
func TestScoreOfRecord_LabelsReplaceJudgeScores(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "truth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	t0 := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	for i, score := range []float64{0.1, 0.2, 0.3, 0.9} {
		id := "s" + string(rune('a'+i))
		if err := insertScoreOnly(archive, id, t0.Add(time.Duration(i)*time.Minute), "acme", "rca", "m", score); err != nil {
			t.Fatal(err)
		}
	}
	half := 0.5
	labels := []*online.Label{
		// sa: a person scored it 0.5 (two labels; the newer one counts).
		{ID: "l1", SessionID: "sa", Tenant: "acme", Kind: "rca", Metric: "m", JudgeScore: 0.1, Verdict: "disagree", CreatedAt: t0},
		{ID: "l2", SessionID: "sa", Tenant: "acme", Kind: "rca", Metric: "m", JudgeScore: 0.1, Verdict: "disagree", Score: &half, Note: "fine", CreatedAt: t0.Add(time.Hour)},
		// sb: disagreed without a score: out.
		{ID: "l3", SessionID: "sb", Tenant: "acme", Kind: "rca", Metric: "m", JudgeScore: 0.2, Verdict: "disagree", CreatedAt: t0},
		// sc: agreed: the judge's 0.3 stands, confirmed.
		{ID: "l4", SessionID: "sc", Tenant: "acme", Kind: "rca", Metric: "m", JudgeScore: 0.3, Verdict: "agree", CreatedAt: t0},
	}
	for _, l := range labels {
		if err := archive.InsertOnlineLabel(l); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := archive.OnlineSamples(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]online.Sample{}
	for _, r := range rows {
		got[r.Sample.SessionID] = r.Sample
	}
	if len(got) != 3 || got["sa"].Score != 0.5 || !got["sa"].Human || got["sa"].Note != "fine" ||
		got["sc"].Score != 0.3 || !got["sc"].Human || got["sd"].Score != 0.9 || got["sd"].Human {
		t.Errorf("samples = %+v", got)
	}
	if _, out := got["sb"]; out {
		t.Error("a disagreement without a score must leave the session out")
	}

	for_, err := archive.OnlineSamplesFor("acme", "rca", "m", "", 0, t0.Add(time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if mean := online.MeanOf(for_); for_ == nil || mean.N != 3 || mean.Mean < 0.566 || mean.Mean > 0.567 {
		t.Errorf("mean of record = %+v, want (0.5+0.3+0.9)/3", mean)
	}

	series, err := archive.OnlineSeries(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].N != 3 || series[0].Mean < 0.566 || series[0].Mean > 0.567 {
		t.Errorf("series = %+v", series)
	}
}
