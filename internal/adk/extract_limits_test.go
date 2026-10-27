package adk

import (
	"strings"
	"testing"
)

// An attribute of tiny elements decodes to many times its size; the converter leaves one over the limit undecoded
// and converts the rest of the trace.
func TestConvertLeavesAnAmplifyingAttributeUndecoded(t *testing.T) {
	hostile := "[" + strings.Repeat("0,", maxAttrJSONElements+1) + "0]"
	if got := parseJSON(hostile); got != nil {
		t.Errorf("parseJSON decoded %T, want nil for an attribute over the element limit", got)
	}
	if got := parseJSON(`[1,2,3]`); got == nil {
		t.Error("parseJSON refused an ordinary attribute")
	}
	if got := parseToolResponseContent(hostile); got["result"] != hostile {
		t.Error("a tool result over the limit should be kept as text, not decoded")
	}
}
