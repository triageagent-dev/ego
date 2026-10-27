package trace

import (
	"bytes"
	"strings"
)

// JSONFits reports whether the JSON text s is small enough to decode into generic values. A decoded value takes many
// times the room of its text (a megabyte of `0,0,0,...` becomes dozens), so the size alone does not bound the
// memory: the number of elements does, and each element needs a comma, an opening bracket or an opening brace.
// Strings with many commas count too, which only makes the check stricter on text no sender writes that way.
func JSONFits(s string, maxBytes, maxElements int) bool {
	if len(s) > maxBytes {
		return false
	}
	return strings.Count(s, ",")+strings.Count(s, "[")+strings.Count(s, "{") <= maxElements
}

// JSONFitsBytes is JSONFits for text already held as bytes.
func JSONFitsBytes(b []byte, maxBytes, maxElements int) bool {
	if len(b) > maxBytes {
		return false
	}
	return bytes.Count(b, []byte(","))+bytes.Count(b, []byte("["))+bytes.Count(b, []byte("{")) <= maxElements
}
