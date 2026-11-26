package adk

import (
	"encoding/json"
	"strconv"
	"strings"

	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// A model call's input is often far longer than what its span records. A sender gives the judges the rest as
// documents on a retrieval span under the call: the facts the answer may rest on, one document per block.
const (
	// EgoEvidenceDocuments is a JSON array of {"id","content"}. One attribute holds any number of documents, where
	// the numbered attributes of OpenInference run into the SDK's limit of attributes a span may have.
	EgoEvidenceDocuments = "ego.evidence.documents"
	// retrievalDocumentsPrefix starts OpenInference's numbered attributes of a RETRIEVER span.
	retrievalDocumentsPrefix = "retrieval.documents."
)

// MaxContextBytes is the most context one turn's judges are given in one call. A turn with more is treated as cut:
// judged on a part of it, an answer would again be read as made up.
const MaxContextBytes = 400_000

// ContextDoc is one document of the context the model was given.
type ContextDoc struct {
	ID   string `json:"id,omitempty"`
	Text string `json:"content"`
}

// SpanDocuments is the context documents span carries, in either form.
func SpanDocuments(span *tracepkg.Span) []ContextDoc {
	var out []ContextDoc
	if raw, ok := span.Tags[EgoEvidenceDocuments]; ok {
		items, isList := raw.([]any)
		if !isList {
			if s, isText := raw.(string); isText {
				_ = json.Unmarshal([]byte(s), &out)
			}
		}
		for _, item := range items {
			if m := asMap(item); m != nil && stringField(m, "content") != "" {
				out = append(out, ContextDoc{ID: stringField(m, "id"), Text: stringField(m, "content")})
			}
		}
	}
	if kind := span.TagString("openinference.span.kind"); !strings.EqualFold(kind, "RETRIEVER") {
		return out
	}
	// The numbering is the sender's; the documents are read in its order until the first gap.
	for i := 0; ; i++ {
		prefix := retrievalDocumentsPrefix + strconv.Itoa(i) + ".document."
		text := span.TagString(prefix + "content")
		if text == "" {
			return out
		}
		out = append(out, ContextDoc{ID: span.TagString(prefix + "id"), Text: text})
	}
}

// IsDocumentAttr reports whether key is an attribute SpanDocuments reads.
func IsDocumentAttr(key string) bool {
	return key == EgoEvidenceDocuments || strings.HasPrefix(key, retrievalDocumentsPrefix)
}

// turnContext is the documents of every span of one turn, each once.
func turnContext(invSpan *tracepkg.Span) []ContextDoc {
	var out []ContextDoc
	seen := map[ContextDoc]bool{}
	var walk func(s *tracepkg.Span)
	walk = func(s *tracepkg.Span) {
		for _, d := range SpanDocuments(s) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
		for _, child := range s.Children {
			walk(child)
		}
	}
	walk(invSpan)
	return out
}

// ContextBytes is the size of the documents' text and ids.
func ContextBytes(docs []ContextDoc) int {
	n := 0
	for _, d := range docs {
		n += len(d.ID) + len(d.Text)
	}
	return n
}
