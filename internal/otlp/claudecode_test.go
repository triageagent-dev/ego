package otlp

import (
	"reflect"
	"strconv"
	"testing"
)

type tagMap = map[string]any

// ccExport builds an OTLP/JSON export with one span named name carrying attrs (Go string, int, float64 and bool
// values become the matching AnyValue) under a resource carrying resource.
func ccExport(name string, attrs, resource tagMap) map[string]any {
	enc := func(m tagMap) []any {
		var out []any
		for k, v := range m {
			var val map[string]any
			switch t := v.(type) {
			case string:
				val = map[string]any{"stringValue": t}
			case int:
				val = map[string]any{"intValue": strconv.Itoa(t)}
			case float64:
				val = map[string]any{"doubleValue": t}
			case bool:
				val = map[string]any{"boolValue": t}
			default:
				panic("unsupported test attribute type")
			}
			out = append(out, map[string]any{"key": k, "value": val})
		}
		return out
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": enc(resource)},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{}, "spans": []any{map[string]any{
			"traceId": "aaaa", "spanId": "s1", "name": name,
			"startTimeUnixNano": "1000000", "endTimeUnixNano": "2000000",
			"attributes": enc(attrs),
		}}}},
	}}}
}

func ccTags(t *testing.T, name string, attrs, resource tagMap) tagMap {
	t.Helper()
	traces := ParseExportRequest(ccExport(name, attrs, resource))
	if len(traces) != 1 || len(traces[0].AllSpans) != 1 {
		t.Fatalf("got %d traces, want one trace with one span", len(traces))
	}
	return traces[0].AllSpans[0].Tags
}

func TestNormalizeClaudeCodeLLMRequest(t *testing.T) {
	tests := []struct {
		name  string
		attrs tagMap
		res   tagMap
		want  tagMap // every key must equal; nil value means the key must be absent
	}{
		{
			name: "input is the sum with both cache counts",
			attrs: tagMap{
				"input_tokens": 100, "output_tokens": 50, "cache_read_tokens": 1000, "cache_creation_tokens": 200,
			},
			want: tagMap{
				"gen_ai.usage.input_tokens":                int64(1300),
				"gen_ai.usage.output_tokens":               int64(50),
				"gen_ai.usage.cache_read.input_tokens":     int64(1000),
				"gen_ai.usage.cache_creation.input_tokens": int64(200),
				"gen_ai.usage.cache_write.input_tokens":    int64(200),
				"input_tokens":                             int64(100), // the original attributes are kept
				"cache_read_tokens":                        int64(1000),
				"gen_ai.operation.name":                    "chat",
				"otel.status_code":                         nil,
				"gen_ai.response.finish_reasons":           nil,
				"gen_ai.request.model":                     nil,
			},
		},
		{
			name:  "no cache attributes: input is input_tokens and no cache attribute appears",
			attrs: tagMap{"input_tokens": 7, "output_tokens": 3},
			want: tagMap{
				"gen_ai.usage.input_tokens":                int64(7),
				"gen_ai.usage.output_tokens":               int64(3),
				"gen_ai.usage.cache_read.input_tokens":     nil,
				"gen_ai.usage.cache_write.input_tokens":    nil,
				"gen_ai.usage.cache_creation.input_tokens": nil,
			},
		},
		{
			name:  "only cache reads still make an input count",
			attrs: tagMap{"cache_read_tokens": 40, "output_tokens": 2},
			want: tagMap{
				"gen_ai.usage.input_tokens":            int64(40),
				"gen_ai.usage.cache_read.input_tokens": int64(40),
			},
		},
		{
			name:  "counts sent as doubles or strings are read",
			attrs: tagMap{"input_tokens": 10.0, "output_tokens": "5", "cache_read_tokens": "20"},
			want: tagMap{
				"gen_ai.usage.input_tokens":  int64(30),
				"gen_ai.usage.output_tokens": int64(5),
			},
		},
		{
			name:  "no token attributes: no usage attributes",
			attrs: tagMap{"model": "claude-x", "success": true},
			want: tagMap{
				"gen_ai.usage.input_tokens":  nil,
				"gen_ai.usage.output_tokens": nil,
				"gen_ai.request.model":       "claude-x",
			},
		},
		{
			name: "existing GenAI attributes are never overwritten",
			attrs: tagMap{
				"input_tokens": 100, "output_tokens": 50, "cache_read_tokens": 10, "cache_creation_tokens": 5,
				"gen_ai.usage.input_tokens":                9,
				"gen_ai.usage.output_tokens":               8,
				"gen_ai.usage.cache_read.input_tokens":     7,
				"gen_ai.usage.cache_creation.input_tokens": 6,
				"gen_ai.operation.name":                    "text_completion",
				"gen_ai.request.model":                     "from-sender",
				"model":                                    "claude-x",
				"gen_ai.response.finish_reasons":           "kept",
				"stop_reason":                              "end_turn",
			},
			want: tagMap{
				"gen_ai.usage.input_tokens":                int64(9),
				"gen_ai.usage.output_tokens":               int64(8),
				"gen_ai.usage.cache_read.input_tokens":     int64(7),
				"gen_ai.usage.cache_creation.input_tokens": int64(6),
				"gen_ai.operation.name":                    "text_completion",
				"gen_ai.request.model":                     "from-sender",
				"gen_ai.response.finish_reasons":           "kept",
				// The other spelling was absent, so it is filled.
				"gen_ai.usage.cache_write.input_tokens": int64(5),
			},
		},
		{
			name:  "model and stop reason map to the request model and finish reasons",
			attrs: tagMap{"model": "claude-x", "stop_reason": "tool_use"},
			want: tagMap{
				"gen_ai.request.model":           "claude-x",
				"gen_ai.response.finish_reasons": []any{"tool_use"},
			},
		},
		{
			name:  "a failed request is an error span",
			attrs: tagMap{"success": false, "error": "overloaded", "status_code": 529},
			want: tagMap{
				"otel.status_code":        "ERROR",
				"otel.status_description": "overloaded",
			},
		},
		{
			name:  "a successful request is not an error span",
			attrs: tagMap{"success": true},
			want:  tagMap{"otel.status_code": nil},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := ccTags(t, "claude_code.llm_request", tc.attrs, tc.res)
			for k, want := range tc.want {
				got, ok := tags[k]
				if want == nil {
					if ok {
						t.Errorf("%s = %v, want absent", k, got)
					}
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v", k, got, want)
				}
			}
		})
	}
}

func TestNormalizeClaudeCodeSession(t *testing.T) {
	tests := []struct {
		name  string
		attrs tagMap
		res   tagMap
		want  any // gen_ai.conversation.id; nil means absent
	}{
		{"session.id on the span", tagMap{"session.id": "cc-1"}, nil, "cc-1"},
		{"session.id on the resource", nil, tagMap{"session.id": "cc-2"}, "cc-2"},
		{"conversation id already set", tagMap{"session.id": "cc-1", "gen_ai.conversation.id": "conv"}, nil, "conv"},
		{"session name already set on the resource", tagMap{"session.id": "cc-1"}, tagMap{"ego.session_name": "named"}, nil},
		{"no session.id at all", tagMap{"model": "m"}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Every claude_code.* span takes the fallback, not only the LLM requests.
			for _, span := range []string{"claude_code.interaction", "claude_code.tool.execution", "claude_code.llm_request"} {
				tags := ccTags(t, span, tc.attrs, tc.res)
				got, ok := tags["gen_ai.conversation.id"]
				if tc.want == nil {
					if ok {
						t.Errorf("%s: conversation id = %v, want absent", span, got)
					}
				} else if got != tc.want {
					t.Errorf("%s: conversation id = %v, want %v", span, got, tc.want)
				}
			}
		})
	}
}

func TestNormalizeClaudeCodeAgentName(t *testing.T) {
	tests := []struct {
		name  string
		attrs tagMap
		res   tagMap
		want  any
	}{
		{"service.name is the default", nil, tagMap{"service.name": "claude-code"}, "claude-code"},
		{"renamed service", nil, tagMap{"service.name": "review-bot"}, "review-bot"},
		{"resource agent name beats service.name", nil, tagMap{"gen_ai.agent.name": "reviewer", "service.name": "claude-code"}, "reviewer"},
		{"span agent name is kept", tagMap{"gen_ai.agent.name": "mine"}, tagMap{"service.name": "claude-code"}, "mine"},
		{"nothing to take it from", nil, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := ccTags(t, "claude_code.interaction", tc.attrs, tc.res)
			got, ok := tags["gen_ai.agent.name"]
			if tc.want == nil {
				if ok {
					t.Errorf("agent name = %v, want absent", got)
				}
			} else if got != tc.want {
				t.Errorf("agent name = %v, want %v", got, tc.want)
			}
		})
	}
}

// A resource-level tenant reaches every span, which is where Claude Code puts OTEL_RESOURCE_ATTRIBUTES.
func TestNormalizeClaudeCodeResourceTenant(t *testing.T) {
	for _, attr := range []string{"ego.tenant", "tenant.id"} {
		tags := ccTags(t, "claude_code.llm_request", tagMap{"input_tokens": 1}, tagMap{attr: "tenant-a"})
		if tags[attr] != "tenant-a" {
			t.Errorf("%s = %v, want tenant-a on the span", attr, tags[attr])
		}
	}
}

func TestNormalizeClaudeCodeTool(t *testing.T) {
	tests := []struct {
		name  string
		attrs tagMap
		want  tagMap
	}{
		{
			name:  "tool name and call id",
			attrs: tagMap{"tool_name": "Bash", "tool_name_safe": "Bash", "tool_use_id": "toolu_1"},
			want: tagMap{
				"gen_ai.operation.name": "execute_tool",
				"gen_ai.tool.name":      "Bash",
				"gen_ai.tool.call.id":   "toolu_1",
			},
		},
		{
			name:  "tool_name_safe when tool_name is absent",
			attrs: tagMap{"tool_name_safe": "mcp_tool", "tool_use_id": "toolu_2"},
			want:  tagMap{"gen_ai.tool.name": "mcp_tool"},
		},
		{
			name: "existing attributes are kept",
			attrs: tagMap{
				"tool_name": "Bash", "tool_use_id": "toolu_3", "gen_ai.tool.name": "mine", "gen_ai.tool.call.id": "id-9",
				"gen_ai.operation.name": "other",
			},
			want: tagMap{"gen_ai.tool.name": "mine", "gen_ai.tool.call.id": "id-9", "gen_ai.operation.name": "other"},
		},
		{
			name:  "no tool name: no tool attributes",
			attrs: tagMap{"duration_ms": 5},
			want:  tagMap{"gen_ai.tool.name": nil, "gen_ai.tool.call.id": nil},
		},
		{
			// Gated content is not mapped: the call has a name and an id, not arguments or output.
			name:  "command and path are not copied into arguments",
			attrs: tagMap{"tool_name": "Bash", "full_command": "ls -l", "file_path": "/x"},
			want:  tagMap{"gen_ai.tool.call.arguments": nil, "input.value": nil, "gen_ai.tool.call.result": nil},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := ccTags(t, "claude_code.tool", tc.attrs, nil)
			for k, want := range tc.want {
				got, ok := tags[k]
				if want == nil {
					if ok {
						t.Errorf("%s = %v, want absent", k, got)
					}
				} else if got != want {
					t.Errorf("%s = %v, want %v", k, got, want)
				}
			}
		})
	}
}

// The child spans of a tool are not tool calls of their own: the tool span is the one call.
func TestNormalizeClaudeCodeToolChildrenAreNotToolCalls(t *testing.T) {
	for _, span := range []string{"claude_code.tool.execution", "claude_code.tool.blocked_on_user"} {
		tags := ccTags(t, span, tagMap{"tool_name": "Bash", "tool_use_id": "toolu_1"}, nil)
		if _, ok := tags["gen_ai.tool.name"]; ok {
			t.Errorf("%s got gen_ai.tool.name", span)
		}
	}
}

func TestNormalizeClaudeCodeUserPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt any // nil means the attribute is not sent
		input  string
		want   any // input.value; nil means absent
	}{
		{"redacted prompt is ignored", "<REDACTED>", "", nil},
		{"redacted with spaces is ignored", " <REDACTED> ", "", nil},
		{"empty prompt is ignored", "", "", nil},
		{"no prompt", nil, "", nil},
		{"real prompt becomes the input", "fix the build", "", "fix the build"},
		{"existing input.value is kept", "fix the build", "mine", "mine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := tagMap{"user_prompt_length": 13}
			if tc.prompt != nil {
				attrs["user_prompt"] = tc.prompt
			}
			if tc.input != "" {
				attrs["input.value"] = tc.input
			}
			tags := ccTags(t, "claude_code.interaction", attrs, nil)
			got, ok := tags["input.value"]
			if tc.want == nil {
				if ok {
					t.Errorf("input.value = %v, want absent", got)
				}
			} else if got != tc.want {
				t.Errorf("input.value = %v, want %v", got, tc.want)
			}
			// The original attribute is left as the sender sent it.
			if tc.prompt != nil && tags["user_prompt"] != tc.prompt {
				t.Errorf("user_prompt = %v, want it unchanged", tags["user_prompt"])
			}
		})
	}
}

// A span from any other sender, whatever its attributes, passes through unchanged.
func TestNormalizeClaudeCodeLeavesOtherSpansAlone(t *testing.T) {
	attrs := tagMap{
		"input_tokens": 100, "output_tokens": 50, "cache_read_tokens": 10, "model": "m", "session.id": "s",
		"tool_name": "Bash", "success": false, "user_prompt": "hello", "stop_reason": "end_turn",
	}
	res := tagMap{"service.name": "claude-code"}
	for _, name := range []string{"llm_request", "claude_code", "my.claude_code.llm_request", "invoke_agent", "Claude_Code.llm_request"} {
		tags := ccTags(t, name, attrs, res)
		want := tagMap{}
		for k, v := range attrs {
			want[k] = v
		}
		want["service.name"] = "claude-code"
		for k := range tags {
			if _, ok := want[k]; !ok {
				t.Errorf("%s: unexpected attribute %s=%v", name, k, tags[k])
			}
		}
		for k, v := range want {
			got := tags[k]
			// Numbers are decoded to int64 on the way in.
			if n, ok := v.(int); ok {
				v = int64(n)
			}
			if got != v {
				t.Errorf("%s: %s = %v, want %v", name, k, got, v)
			}
		}
	}
}
