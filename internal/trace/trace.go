// Package trace holds the normalized, format-agnostic span/trace model that every loader (Jaeger JSON today,
// OTLP later) converts into.
package trace

// Span is a normalized trace span: flat attribute map plus a parent-resolved
// children slice, independent of the wire format it was loaded from.
type Span struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	OperationName string
	StartTime     int64 // microseconds since epoch
	Duration      int64 // microseconds
	Tags          map[string]any
	Children      []*Span
}

// Tag returns the raw attribute value for key, or nil if absent.
func (s *Span) Tag(key string) any {
	if s == nil {
		return nil
	}
	return s.Tags[key]
}

// TagString returns the attribute value as a string, or "" if absent or not
// a string (e.g. numeric/bool attributes).
func (s *Span) TagString(key string) string {
	v, _ := s.Tag(key).(string)
	return v
}

// HasTag reports whether key is present on the span, regardless of value.
func (s *Span) HasTag(key string) bool {
	_, ok := s.Tags[key]
	return ok
}

// EndTime is StartTime + Duration, in microseconds since epoch.
func (s *Span) EndTime() int64 {
	return s.StartTime + s.Duration
}

// Trace is a single trace: its root spans (no resolved parent within the
// trace) and the flat list of every span in it.
type Trace struct {
	TraceID   string
	RootSpans []*Span
	AllSpans  []*Span
}
