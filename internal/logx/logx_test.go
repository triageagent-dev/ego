package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "info": true, "DEBUG": true, " warn ": true, "warning": true, "error": true, "trace": false, "3": false} {
		if _, err := ParseLevel(in); (err == nil) != ok {
			t.Errorf("ParseLevel(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}

func TestThresholdFiltersLowerLevels(t *testing.T) {
	var buf bytes.Buffer
	if err := setup("warn", &buf); err != nil {
		t.Fatal(err)
	}
	Debugf("d %d", 1)
	Infof("i %d", 2)
	Warnf("w %d", 3)
	Errorf("e %d", 4)
	out := buf.String()
	if strings.Contains(out, "d 1") || strings.Contains(out, "i 2") {
		t.Errorf("below-threshold lines were written: %q", out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "w 3") || !strings.Contains(out, "level=ERROR") {
		t.Errorf("warn and error lines missing: %q", out)
	}
	if err := setup("debug", &buf); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	Debugf("shown")
	if !strings.Contains(buf.String(), "level=DEBUG") {
		t.Errorf("debug not written at debug level: %q", buf.String())
	}
}
