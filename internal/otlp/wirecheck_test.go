package otlp

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// wrap puts inner in a length-delimited field of the given number.
func wrap(num protowire.Number, inner []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), inner)
}

// denseExport is an ExportTraceServiceRequest whose one span holds n empty attributes: two bytes of wire each.
func denseExport(n int) []byte {
	attrs := bytes.Repeat([]byte{0x4a, 0x00}, n) // Span.attributes is field 9, a length-delimited empty KeyValue
	return wrap(1, wrap(2, wrap(2, attrs)))      // resource_spans > scope_spans > spans
}

func allocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A real export, captured from a LangGraph agent, decodes.
func TestDecodeProtobufTracesAcceptsARealExport(t *testing.T) {
	raw, err := os.ReadFile("testdata/langgraph_langsmith.pb")
	if err != nil {
		t.Fatal(err)
	}
	if ExportTooDense(raw) {
		t.Fatal("a real export was judged too dense")
	}
	body, err := DecodeProtobufTraces(raw)
	if err != nil {
		t.Fatal(err)
	}
	if traces := ParseExportRequest(body); len(traces) != 1 || len(traces[0].AllSpans) != 9 {
		t.Errorf("decoded %d traces, want one of 9 spans", len(traces))
	}
}

// Seven megabytes of empty messages took a gigabyte to decode. They are refused before anything is allocated for them.
func TestDecodeProtobufTracesRefusesTinyMessagesBeforeDecodingThem(t *testing.T) {
	raw := denseExport(3_500_000) // 7 MB
	var err error
	bytes := allocatedBytes(func() { _, err = DecodeProtobufTraces(raw) })
	if !errors.Is(err, ErrBodyTooDense) {
		t.Fatalf("err = %v, want the dense-body refusal", err)
	}
	if bytes > 16<<20 {
		t.Errorf("refusing it allocated %d MiB, want next to nothing", bytes>>20)
	}
	if ExportTooDense(denseExport(1000)) {
		t.Error("a small export was judged too dense")
	}
	if ExportTooDense([]byte{0xff, 0xff, 0xff}) {
		t.Error("bytes that are not a message are proto.Unmarshal's to report, not this check's")
	}
}

func TestDecodeJSONBody(t *testing.T) {
	real, err := os.ReadFile("testdata/langgraph_langsmith.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJSONBody(real); err != nil {
		t.Errorf("a real export: %v", err)
	}
	hostile := []byte(`{"resourceSpans":[` + strings.Repeat("0,", 2<<20) + `0]}`) // 4 MiB, one element per 2 bytes
	var derr error
	bytes := allocatedBytes(func() { _, derr = DecodeJSONBody(hostile) })
	if !errors.Is(derr, ErrBodyTooDense) {
		t.Fatalf("err = %v, want the dense-body refusal", derr)
	}
	if bytes > 16<<20 {
		t.Errorf("refusing it allocated %d MiB, want next to nothing", bytes>>20)
	}
	// However sparse, a body does not get past the absolute limit.
	big := []byte(`{"a":[` + strings.Repeat("0,      ", maxBodyElements+100_000) + `0]}`) // 8 bytes an element, over the cap
	if _, err := DecodeJSONBody(big); !errors.Is(err, ErrBodyTooDense) {
		t.Errorf("a body over the element cap: err = %v", err)
	}
	if _, err := DecodeJSONBody([]byte(`{`)); err == nil || errors.Is(err, ErrBodyTooDense) {
		t.Errorf("a malformed body: err = %v, want a JSON error", err)
	}
}
