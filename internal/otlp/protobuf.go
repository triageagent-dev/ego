package otlp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func DecodeProtobufTraces(raw []byte) (map[string]any, error) {
	if ExportTooDense(raw) {
		return nil, ErrBodyTooDense
	}
	msg := &coltracepb.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("decoding protobuf ExportTraceServiceRequest: %w", err)
	}
	return ExportRequestToMap(msg)
}

// ExportRequestToMap converts an already-parsed ExportTraceServiceRequest
// to the same JSON-dict shape DecodeProtobufTraces produces from raw bytes.
// Factored out so the OTLP/gRPC receiver (internal/api's grpc.go), which
// gets the message pre-decoded by the grpc package instead of raw bytes on
// the wire, can feed ParseExportRequest without a redundant
// marshal-to-bytes-then-unmarshal round trip through DecodeProtobufTraces.
func ExportRequestToMap(msg *coltracepb.ExportTraceServiceRequest) (map[string]any, error) {
	jsonBytes, err := protojson.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshaling protobuf to JSON: %w", err)
	}

	data, err := DecodeJSONBody(jsonBytes)
	if err != nil {
		return nil, fmt.Errorf("decoding protojson output: %w", err)
	}

	fixProtobufIDFields(data)
	return data, nil
}

// fixProtobufIDFields converts base64-encoded bytes fields to hex strings in place. protojson (like
// MessageToDict) base64-encodes protobuf bytes fields, but OTLP/JSON uses hex-encoded strings for traceId,
// spanId, and parentSpanId.
func fixProtobufIDFields(data any) {
	switch v := data.(type) {
	case map[string]any:
		for _, key := range []string{"traceId", "spanId", "parentSpanId"} {
			if s, ok := v[key].(string); ok {
				if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
					v[key] = fmt.Sprintf("%x", raw)
				}
			}
		}
		for _, value := range v {
			fixProtobufIDFields(value)
		}
	case []any:
		for _, item := range v {
			fixProtobufIDFields(item)
		}
	}
}

// A real OTLP/JSON export has an element (a comma, an opening bracket or an opening brace) about every 20 to 30
// bytes; a body of tiny elements has one every 2 and decodes to about 120 times its size, which the pod's memory
// does not stand. A body is decoded only if it has no more than one element per 8 bytes, and never more than
// maxBodyElements however large it is.
const (
	minBytesPerElement = 8
	maxBodyElements    = 2_000_000
)

// ErrBodyTooDense is returned for a body that would decode to much more than its size.
var ErrBodyTooDense = errors.New("the body has too many elements for its size")

// DecodeJSONBody decodes an OTLP/JSON export into the generic form ParseExportRequest reads, refusing a body whose
// elements are denser than a trace's can be.
func DecodeJSONBody(raw []byte) (map[string]any, error) {
	limit := min(len(raw)/minBytesPerElement+1000, maxBodyElements)
	if !tracepkg.JSONFitsBytes(raw, len(raw), limit) {
		return nil, ErrBodyTooDense
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}
