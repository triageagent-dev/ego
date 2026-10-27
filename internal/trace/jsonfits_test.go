package trace

import (
	"strings"
	"testing"
)

func TestJSONFits(t *testing.T) {
	for name, tc := range map[string]struct {
		in       string
		bytes    int
		elements int
		want     bool
	}{
		"a small object":                {`{"a":[1,2,3],"b":{"c":"d"}}`, 1000, 100, true},
		"over the size":                 {strings.Repeat("a", 11), 10, 100, false},
		"a short text of many elements": {"[" + strings.Repeat("0,", 500) + "0]", 10000, 100, false},
		"nested openings count":         {strings.Repeat("[", 50), 10000, 10, false},
		"a long string is one element":  {`{"text":"` + strings.Repeat("a", 5000) + `"}`, 10000, 3, true},
		"exactly the limit is allowed":  {`[1,2,3]`, 100, 3, true},
		"one over the limit is refused": {`[1,2,3,4]`, 100, 3, false},
	} {
		if got := JSONFits(tc.in, tc.bytes, tc.elements); got != tc.want {
			t.Errorf("%s: JSONFits = %v, want %v", name, got, tc.want)
		}
	}
}
