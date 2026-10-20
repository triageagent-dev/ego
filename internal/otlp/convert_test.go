package otlp

import "testing"

// sampleExport builds a minimal OTLP/JSON export dict: one resourceSpans
// batch, one scope, one root span, matching the shape a real OTLP/HTTP
// exporter or protojson.Marshal would produce.
func sampleExport() map[string]any {
	return map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "test-agent"}},
						map[string]any{"key": "ego.session_name", "value": map[string]any{"stringValue": "sess-1"}},
					},
				},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{"name": "test-scope", "version": "1.0"},
						"spans": []any{
							map[string]any{
								"traceId":           "3e289017fe03ffd7c4145316d2eb3d0d",
								"spanId":            "e37fdd8f56146d31",
								"name":              "invoke_agent test",
								"startTimeUnixNano": "1000000000",
								"endTimeUnixNano":   "1002000000",
								"attributes": []any{
									map[string]any{"key": "gen_ai.request.model", "value": map[string]any{"stringValue": "gemini-2.0-flash"}},
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestParseExportRequest(t *testing.T) {
	traces := ParseExportRequest(sampleExport())
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]
	if tr.TraceID != "3e289017fe03ffd7c4145316d2eb3d0d" {
		t.Errorf("trace ID = %q", tr.TraceID)
	}
	if len(tr.AllSpans) != 1 {
		t.Fatalf("got %d spans, want 1", len(tr.AllSpans))
	}
	span := tr.AllSpans[0]

	if span.OperationName != "invoke_agent test" {
		t.Errorf("operation name = %q", span.OperationName)
	}
	if span.StartTime != 1_000_000 { // 1_000_000_000ns / 1000 = 1_000_000us
		t.Errorf("start time = %d, want 1000000", span.StartTime)
	}
	if span.Duration != 2000 { // (1_002_000_000 - 1_000_000_000)ns / 1000 = 2000us
		t.Errorf("duration = %d, want 2000", span.Duration)
	}

	// Span-level attribute survives.
	if span.Tags["gen_ai.request.model"] != "gemini-2.0-flash" {
		t.Errorf("gen_ai.request.model = %v", span.Tags["gen_ai.request.model"])
	}
	// Scope name/version injected as span attributes.
	if span.Tags[otelScope] != "test-scope" {
		t.Errorf("otel.scope.name = %v", span.Tags[otelScope])
	}
	if span.Tags[otelScopeVersion] != "1.0" {
		t.Errorf("otel.scope.version = %v", span.Tags[otelScopeVersion])
	}
	// Resource attributes merged onto every span, and win on collision.
	if span.Tags["service.name"] != "test-agent" {
		t.Errorf("service.name = %v", span.Tags["service.name"])
	}
	if span.Tags["ego.session_name"] != "sess-1" {
		t.Errorf("ego.session_name = %v", span.Tags["ego.session_name"])
	}

	if len(tr.RootSpans) != 1 {
		t.Fatalf("got %d root spans, want 1 (no parentSpanId)", len(tr.RootSpans))
	}
}

func TestParseExportRequest_ResourceAttrsWinOnCollision(t *testing.T) {
	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "resource-name"}},
					},
				},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{},
						"spans": []any{
							map[string]any{
								"traceId": "aaaa", "spanId": "root", "name": "root",
								"startTimeUnixNano": "1000", "endTimeUnixNano": "2000",
								"attributes": []any{
									map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "span-level-name"}},
								},
							},
						},
					},
				},
			},
		},
	}

	traces := ParseExportRequest(body)
	got := traces[0].AllSpans[0].Tags["service.name"]
	if got != "resource-name" {
		t.Errorf("service.name = %v, want resource value \"resource-name\" to win over span-level value", got)
	}
}

func TestParseExportRequest_ParentChildLinking(t *testing.T) {
	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{},
						"spans": []any{
							map[string]any{
								"traceId": "aaaa", "spanId": "root", "name": "root",
								"startTimeUnixNano": "1000", "endTimeUnixNano": "5000",
							},
							map[string]any{
								"traceId": "aaaa", "spanId": "child", "parentSpanId": "root", "name": "child",
								"startTimeUnixNano": "2000", "endTimeUnixNano": "3000",
							},
						},
					},
				},
			},
		},
	}

	traces := ParseExportRequest(body)
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]
	if len(tr.RootSpans) != 1 || tr.RootSpans[0].SpanID != "root" {
		t.Fatalf("root spans = %v, want [root]", tr.RootSpans)
	}
	if len(tr.RootSpans[0].Children) != 1 || tr.RootSpans[0].Children[0].SpanID != "child" {
		t.Fatalf("root's children = %v, want [child]", tr.RootSpans[0].Children)
	}
}

func TestParseExportRequest_KeepsStatusMessageAndException(t *testing.T) {
	body := map[string]any{
		"resourceSpans": []any{
			map[string]any{
				"resource": map[string]any{},
				"scopeSpans": []any{
					map[string]any{
						"scope": map[string]any{},
						"spans": []any{
							map[string]any{
								"traceId": "aaaa", "spanId": "root", "name": "llm",
								"startTimeUnixNano": "1000", "endTimeUnixNano": "2000",
								"status": map[string]any{"code": float64(2), "message": "quota exceeded"},
								"events": []any{
									map[string]any{"name": "log", "attributes": []any{
										map[string]any{"key": "exception.type", "value": map[string]any{"stringValue": "not-an-exception"}},
									}},
									map[string]any{"name": "exception", "attributes": []any{
										map[string]any{"key": "exception.type", "value": map[string]any{"stringValue": "ResourceExhausted"}},
										map[string]any{"key": "exception.message", "value": map[string]any{"stringValue": "429"}},
									}},
								},
							},
						},
					},
				},
			},
		},
	}

	tags := ParseExportRequest(body)[0].AllSpans[0].Tags
	for k, want := range map[string]string{
		"otel.status_code":        "ERROR",
		"otel.status_description": "quota exceeded",
		"exception.type":          "ResourceExhausted",
		"exception.message":       "429",
	} {
		if got := tags[k]; got != want {
			t.Errorf("%s = %v, want %q", k, got, want)
		}
	}
}
