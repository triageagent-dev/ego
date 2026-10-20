// Package otlp decodes OTLP export payloads - both the OTLP/JSON wire shape and protobuf (converted to the same
// JSON shape via protojson.
package otlp

import (
	"sort"
	"strconv"

	"github.com/triageagent-dev/ego/internal/logx"
)

// anyValueFields are the OTLP AnyValue union's possible keys, in the shape
// protojson/MessageToDict and native OTLP/JSON both produce.
var anyValueFields = []string{
	"stringValue", "intValue", "doubleValue", "boolValue",
	"kvlistValue", "arrayValue", "bytesValue",
}

// isAnyValue reports whether valueObj carries one of the AnyValue union fields.
func isAnyValue(valueObj map[string]any) bool {
	for _, f := range anyValueFields {
		if _, ok := valueObj[f]; ok {
			return true
		}
	}
	return false
}

// decodeAnyValue recursively decodes an OTLP AnyValue to a native Go value. bytesValue is left as the base64
// string protojson/MessageToDict already encoded it as. A value carrying none of the union fields is returned
// as-is.
func decodeAnyValue(valueObj map[string]any) any {
	if v, ok := valueObj["stringValue"]; ok {
		return v
	}
	if v, ok := valueObj["intValue"]; ok {
		return decodeIntValue(v)
	}
	if v, ok := valueObj["doubleValue"]; ok {
		return v
	}
	if v, ok := valueObj["boolValue"]; ok {
		return v
	}
	if v, ok := valueObj["kvlistValue"]; ok {
		kv, _ := v.(map[string]any)
		result := map[string]any{}
		for _, item := range asSlice(kv["values"]) {
			im, _ := item.(map[string]any)
			key, _ := im["key"].(string)
			result[key] = decodeAnyValue(asMap(im["value"]))
		}
		return result
	}
	if v, ok := valueObj["arrayValue"]; ok {
		arr, _ := v.(map[string]any)
		var result []any
		for _, item := range asSlice(arr["values"]) {
			result = append(result, decodeAnyValue(asMap(item)))
		}
		return result
	}
	if v, ok := valueObj["bytesValue"]; ok {
		return v
	}
	return valueObj
}

// specContainerAttrs is the allowlist of attribute keys permitted to carry a list/map value; every other key
// with a container value is dropped.
var specContainerAttrs = map[string]bool{
	"gen_ai.response.finish_reasons": true,
	"gen_ai.input.messages":          true,
	"gen_ai.output.messages":         true,
	"gen_ai.tool.definitions":        true,
	"gen_ai.system_instructions":     true,
	"gen_ai.tool.call.arguments":     true,
	"gen_ai.tool.call.result":        true,
}

// decodeAttribute decodes one attribute, applying the container allowlist.
func decodeAttribute(key string, valueObj map[string]any) (keep bool, value any) {
	value = decodeAnyValue(valueObj)
	switch value.(type) {
	case map[string]any, []any:
		if !specContainerAttrs[key] {
			logx.Debugf("otlp: dropping container value for %s; only spec container attributes are kept", key)
			return false, nil
		}
	}
	return true, value
}

// DecodeAttributes decodes an OTLP attributes array ([{"key": ..., "value": {...}}, ...]) into a flat map.
func DecodeAttributes(attrsList []any) map[string]any {
	return decodeAttributes(attrsList)
}

func decodeAttributes(attrsList []any) map[string]any {
	result := map[string]any{}
	for _, a := range attrsList {
		attr, ok := a.(map[string]any)
		if !ok {
			continue
		}
		valueObj := asMap(attr["value"])
		if !isAnyValue(valueObj) {
			continue
		}
		key, _ := attr["key"].(string)
		if keep, value := decodeAttribute(key, valueObj); keep {
			result[key] = value
		}
	}
	return result
}

// decodeIntValue normalizes OTLP's intValue, which proto3 JSON mapping encodes as a string (protojson) but
// which some hand-written OTLP/JSON payloads may send as a bare number.
func decodeIntValue(v any) int64 {
	switch t := v.(type) {
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return n
	case float64:
		return int64(t)
	default:
		return 0
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// encodeAnyValue is the inverse of decodeAnyValue: it wraps a native Go
// value (as produced by decodeAttributes, or plain JSON-decoded data) back
// into an OTLP AnyValue object. Used to re-serialize a normalized Span's
// Tags map as OTLP/JSON attributes, e.g. when handing a live session's
// trace back to the UI/re-upload pipeline (see api's get-trace handler).
func encodeAnyValue(v any) map[string]any {
	switch t := v.(type) {
	case nil:
		return map[string]any{"stringValue": ""}
	case string:
		return map[string]any{"stringValue": t}
	case bool:
		return map[string]any{"boolValue": t}
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(t, 10)}
	case int:
		return map[string]any{"intValue": strconv.Itoa(t)}
	case float64:
		if t == float64(int64(t)) {
			return map[string]any{"intValue": strconv.FormatInt(int64(t), 10)}
		}
		return map[string]any{"doubleValue": t}
	case map[string]any:
		values := make([]any, 0, len(t))
		for k, item := range t {
			values = append(values, map[string]any{"key": k, "value": encodeAnyValue(item)})
		}
		return map[string]any{"kvlistValue": map[string]any{"values": values}}
	case []any:
		values := make([]any, 0, len(t))
		for _, item := range t {
			values = append(values, encodeAnyValue(item))
		}
		return map[string]any{"arrayValue": map[string]any{"values": values}}
	default:
		return map[string]any{"stringValue": ""}
	}
}

// EncodeAttributes is the inverse of DecodeAttributes: it turns a flat
// attribute map back into an OTLP attributes array
// ([{"key": ..., "value": {...}}, ...]), sorted by key for deterministic
// output.
func EncodeAttributes(attrs map[string]any) []any {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]any, 0, len(attrs))
	for _, k := range keys {
		result = append(result, map[string]any{"key": k, "value": encodeAnyValue(attrs[k])})
	}
	return result
}
