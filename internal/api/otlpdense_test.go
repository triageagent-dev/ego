package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"
)

func denseProtobufExport(n int) []byte {
	wrap := func(num protowire.Number, inner []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), inner)
	}
	return wrap(1, wrap(2, wrap(2, bytes.Repeat([]byte{0x4a, 0x00}, n)))) // n empty attributes on one span
}

func postOTLP(h http.HandlerFunc, contentType string, body []byte) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code
}

// A sender holding a token can post a few megabytes of tiny elements that decode to a thousand times their size. The
// receiver refuses such a body, in either encoding, and takes a real export in both.
func TestOTLPHTTPRefusesABodyOfTinyElementsAndTakesARealOne(t *testing.T) {
	store := NewSessionStore(nil)
	h := otlpTracesHandler(store)

	if code := postOTLP(h, "application/json", []byte(`{"resourceSpans":[`+strings.Repeat("0,", 2<<20)+`0]}`)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("JSON body of tiny elements = %d, want 413", code)
	}
	if code := postOTLP(h, "application/x-protobuf", denseProtobufExport(3_500_000)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("protobuf body of tiny messages = %d, want 413", code)
	}

	real, err := os.ReadFile("../otlp/testdata/langgraph_langsmith.pb")
	if err != nil {
		t.Fatal(err)
	}
	if code := postOTLP(h, "application/x-protobuf", real); code != http.StatusOK {
		t.Errorf("a real protobuf export = %d, want 200", code)
	}
	realJSON, err := os.ReadFile("../otlp/testdata/langgraph_langsmith.json")
	if err != nil {
		t.Fatal(err)
	}
	if code := postOTLP(h, "application/json", realJSON); code != http.StatusOK {
		t.Errorf("a real JSON export = %d, want 200", code)
	}
	if len(store.List()) == 0 {
		t.Error("the real exports were accepted but made no session")
	}
	if code := postOTLP(h, "application/json", []byte(`{`)); code != http.StatusBadRequest {
		t.Errorf("a malformed JSON body = %d, want 400", code)
	}
}

// The gRPC server unmarshals a message before the handler runs, so the check is in its codec.
func TestOTLPGRPCCodecRefusesTinyMessagesBeforeUnmarshalling(t *testing.T) {
	codec := denseGuardCodec{encodingProto()}
	var req coltracepb.ExportTraceServiceRequest
	if err := codec.Unmarshal(mem.BufferSlice{mem.SliceBuffer(denseProtobufExport(3_500_000))}, &req); err != otlp.ErrBodyTooDense {
		t.Errorf("a dense message: err = %v, want the dense-body refusal", err)
	}
	if len(req.ResourceSpans) != 0 {
		t.Error("the dense message was unmarshalled")
	}
	real, err := os.ReadFile("../otlp/testdata/langgraph_langsmith.pb")
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Unmarshal(mem.BufferSlice{mem.SliceBuffer(real)}, &req); err != nil || len(req.ResourceSpans) != 1 {
		t.Errorf("a real message: %d resource spans, err %v", len(req.ResourceSpans), err)
	}
}

// The decode budget is the request's, not each resource's: an export of many resources, each with a payload that fits
// its own limit, may not decode more than one budget's worth in all.
func TestIngestSharesOneDecodeBudgetAcrossTheResourcesOfAnExport(t *testing.T) {
	payload := `{"messages":[["user","` + strings.Repeat("a", 900_000) + `"]]}`
	resource := func(i int) any {
		return map[string]any{
			"resource": map[string]any{"attributes": []any{
				map[string]any{"key": sessionNameAttr, "value": map[string]any{"stringValue": "s"}},
			}},
			"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
				"traceId": "aaaa", "spanId": "span" + strings.Repeat("x", i), "name": "ChatModel",
				"startTimeUnixNano": "1000000000", "endTimeUnixNano": "1010000000",
				"attributes": []any{
					map[string]any{"key": "langsmith.span.kind", "value": map[string]any{"stringValue": "llm"}},
					map[string]any{"key": "gen_ai.prompt", "value": map[string]any{"stringValue": payload}},
				},
			}}}},
		}
	}
	var resources []any
	for i := 0; i < 8; i++ {
		resources = append(resources, resource(i))
	}
	store := NewSessionStore(nil)
	store.Ingest(map[string]any{"resourceSpans": resources})

	translated := 0
	tr, ok := store.Trace("s")
	if !ok {
		t.Fatal("no session")
	}
	for _, sp := range tr.AllSpans {
		if _, has := sp.Tags["gen_ai.input.messages"]; has {
			translated++
		}
	}
	if translated != 4 {
		t.Errorf("%d of 8 payloads translated, want the 4 that fit one request's 4 MiB", translated)
	}
}
