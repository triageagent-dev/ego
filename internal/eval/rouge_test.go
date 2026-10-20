package eval

import "testing"

func TestRouge1FMeasure(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		reference string
		want      float64
	}{
		{"identical text scores 1.0", "the quick brown fox", "the quick brown fox", 1.0},
		{"empty candidate scores 0", "", "the quick brown fox", 0.0},
		{"empty reference scores 0", "the quick brown fox", "", 0.0},
		{"disjoint text scores 0", "alpha beta gamma", "delta epsilon zeta", 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rouge1FMeasure(tt.candidate, tt.reference)
			if got != tt.want {
				t.Errorf("rouge1FMeasure(%q, %q) = %v, want %v", tt.candidate, tt.reference, got, tt.want)
			}
		})
	}
}

func TestRouge1FMeasure_PartialOverlap(t *testing.T) {
	// candidate shares "the" and "cat" with reference but not "sat"/"mat".
	got := rouge1FMeasure("the cat ran fast", "the cat sat on the mat")
	if got <= 0 || got >= 1 {
		t.Errorf("rouge1FMeasure partial overlap = %v, want in (0, 1)", got)
	}
}
