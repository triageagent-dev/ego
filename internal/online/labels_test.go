package online

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func lbl(id, session, metric, verdict string, at time.Time) Label {
	return Label{ID: id, SessionID: session, Metric: metric, Verdict: verdict, JudgeScore: 0.5, CreatedAt: at}
}

func TestAgreementCountsTheLatestLabelOfEachSessionAndMetric(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	labels := []Label{
		lbl("a", "s1", "m", VerdictDisagree, t0),
		lbl("b", "s1", "m", VerdictAgree, t0.Add(time.Minute)), // the person changed their mind
		lbl("c", "s2", "m", VerdictDisagree, t0),
		lbl("d", "s2", "m", VerdictDisagree, t0.Add(time.Second)), // the same label twice counts once
		lbl("e", "s3", "other", VerdictAgree, t0),
	}
	got := AgreementByMetric(labels)
	if got["m"] != (Agreement{Agree: 1, Disagree: 1}) || got["other"] != (Agreement{Agree: 1}) {
		t.Fatalf("agreement = %+v", got)
	}
	if rate, ok := got["m"].Rate(); !ok || rate != 0.5 {
		t.Errorf("rate = %v %v", rate, ok)
	}
	if _, ok := (Agreement{}).Rate(); ok {
		t.Error("no labels have no rate")
	}
}

func TestGateNeedsTenLabelsAndAgreementBelowTheFloor(t *testing.T) {
	cases := []struct {
		a     Agreement
		floor float64
		want  bool
	}{
		{Agreement{Agree: 2, Disagree: 7}, 0.7, false},  // 9 labels: too few to judge a judge
		{Agreement{Agree: 3, Disagree: 7}, 0.7, true},   // 10 labels at 0.3
		{Agreement{Agree: 7, Disagree: 3}, 0.7, false},  // exactly at the floor is calibrated
		{Agreement{Agree: 69, Disagree: 31}, 0.7, true}, // 0.69
		{Agreement{}, 0.7, false},
	}
	for _, c := range cases {
		if got := c.a.Gated(c.floor); got != c.want {
			t.Errorf("%+v gated = %v, want %v", c.a, got, c.want)
		}
	}
}

func TestPickAnchorsTakesTheLongestWaitingFirst(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	var labels []Label
	for i, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		labels = append(labels, lbl("l"+id, id, "m", VerdictAgree, t0.Add(time.Duration(i)*time.Minute)))
	}
	// s1 and s2 were re-scored (s2 longer ago); s3..s5 never were, so they come first, oldest label first.
	rescored := map[string]time.Time{
		AnchorKey("s1", "m"): t0.Add(2 * time.Hour),
		AnchorKey("s2", "m"): t0.Add(time.Hour),
	}
	var ids []string
	for _, l := range PickAnchors(labels, rescored, 4) {
		ids = append(ids, l.SessionID)
	}
	if want := []string{"s3", "s4", "s5", "s2"}; !slices.Equal(ids, want) {
		t.Errorf("anchors = %v, want %v", ids, want)
	}
	if n := len(PickAnchors(labels, nil, 20)); n != 5 {
		t.Errorf("fewer labels than the cap = %d anchors", n)
	}
}

func TestLabelReferenceAndMeanAbsDiff(t *testing.T) {
	score := 0.9
	if r := (Label{JudgeScore: 0.2, Score: &score}).Reference(); r != 0.9 {
		t.Errorf("reference with a score = %v", r)
	}
	if r := (Label{JudgeScore: 0.2}).Reference(); r != 0.2 {
		t.Errorf("reference without a score = %v", r)
	}
	if d := MeanAbsDiff([]float64{1, 0}, []float64{0.5, 0.25}); d != 0.375 {
		t.Errorf("mean abs diff = %v", d)
	}
	if MeanAbsDiff(nil, nil) != 0 {
		t.Error("no pairs, no drift")
	}
}

func TestDigestShowsAgreementPerMetric(t *testing.T) {
	text := DigestText(nil, []AgreementRow{
		{Metric: "b_metric", Agreement: Agreement{Agree: 3, Disagree: 7}, Gated: true},
		{Metric: "a_metric", Agreement: Agreement{Agree: 4, Disagree: 1}},
	}, BudgetStatus{}, 0, "")
	for _, want := range []string{"`a_metric` 0.80 (4 agree, 1 disagree", "`b_metric` 0.30 (3 agree, 7 disagree", "not used for findings"} {
		if !strings.Contains(text, want) {
			t.Errorf("digest lacks %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "a_metric") > strings.Index(text, "b_metric") {
		t.Error("metrics are not sorted")
	}
}
