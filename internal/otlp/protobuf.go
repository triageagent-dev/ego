package otlp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func DecodeProtobufTraces(raw []byte) (map[string]any, error) {
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

	var data map[string]any
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
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
