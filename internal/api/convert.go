package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/loader"
	tracepkg "github.com/triageagent-dev/ego/internal/trace"
)

// This file implements POST /api/convert: converting uploaded trace files to invocations plus per-trace
// agent-identity metadata, without running any evaluation. The UI (ui/src/context/TraceProvider.tsx's
// setTraceFiles) calls this before every evaluation run and keys the dashboard table's Name/Start Time/
// Conversation/Model/Session ID columns off its response - without this endpoint those columns spin forever
// (see README.md's "/api/convert" gap note, now closed by this file).

// traceConversionMetadataDTO matches ui/src/lib/types.ts's
// TraceConversionMetadata.
type traceConversionMetadataDTO struct {
	AgentName          string `json:"agentName,omitempty"`
	Model              string `json:"model,omitempty"`
	StartTime          int64  `json:"startTime,omitempty"`
	UserInputPreview   string `json:"userInputPreview,omitempty"`
	FinalOutputPreview string `json:"finalOutputPreview,omitempty"`
	SessionName        string `json:"sessionName,omitempty"`
}

// traceConversionEntryDTO matches ui/src/lib/types.ts's TraceConversionEntry.
type traceConversionEntryDTO struct {
	TraceID     string                     `json:"traceId"`
	Invocations []convertInvocationDTO     `json:"invocations"`
	Warnings    []string                   `json:"warnings"`
	Metadata    traceConversionMetadataDTO `json:"metadata"`
}

type convertTracesDataDTO struct {
	Traces []traceConversionEntryDTO `json:"traces"`
}

// convertPartDTO/convertContentDTO/convertFunctionCallDTO/
// convertFunctionResponseDTO/convertIntermediateDataDTO/ convertInvocationDTO are camelCase-keyed.ts's
// Invocation/Content/Part/... expect. Nested tool args/response maps are passed through unconverted (.
type convertPartDTO struct {
	Text             string                      `json:"text,omitempty"`
	FunctionCall     *convertFunctionCallDTO     `json:"functionCall,omitempty"`
	FunctionResponse *convertFunctionResponseDTO `json:"functionResponse,omitempty"`
}

type convertContentDTO struct {
	Role  string           `json:"role"`
	Parts []convertPartDTO `json:"parts"`
}

type convertFunctionCallDTO struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type convertFunctionResponseDTO struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type convertIntermediateDataDTO struct {
	ToolUses      []convertFunctionCallDTO     `json:"toolUses,omitempty"`
	ToolResponses []convertFunctionResponseDTO `json:"toolResponses,omitempty"`
}

type convertInvocationDTO struct {
	InvocationID      string                      `json:"invocationId,omitempty"`
	UserContent       *convertContentDTO          `json:"userContent,omitempty"`
	FinalResponse     *convertContentDTO          `json:"finalResponse,omitempty"`
	IntermediateData  *convertIntermediateDataDTO `json:"intermediateData,omitempty"`
	CreationTimestamp float64                     `json:"creationTimestamp,omitempty"`
}

func toConvertContentDTO(c *adk.Content) *convertContentDTO {
	if c == nil {
		return nil
	}
	parts := make([]convertPartDTO, len(c.Parts))
	for i, p := range c.Parts {
		dto := convertPartDTO{Text: p.Text}
		if p.FunctionCall != nil {
			dto.FunctionCall = &convertFunctionCallDTO{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args}
		}
		if p.FunctionResponse != nil {
			dto.FunctionResponse = &convertFunctionResponseDTO{ID: p.FunctionResponse.ID, Name: p.FunctionResponse.Name, Response: p.FunctionResponse.Response}
		}
		parts[i] = dto
	}
	return &convertContentDTO{Role: c.Role, Parts: parts}
}

func toConvertInvocationDTO(inv adk.Invocation) convertInvocationDTO {
	dto := convertInvocationDTO{
		InvocationID:      inv.InvocationID,
		UserContent:       toConvertContentDTO(inv.UserContent),
		FinalResponse:     toConvertContentDTO(inv.FinalResponse),
		CreationTimestamp: inv.CreationTimestamp,
	}
	if inv.IntermediateData != nil {
		toolUses := make([]convertFunctionCallDTO, len(inv.IntermediateData.ToolUses))
		for i, tu := range inv.IntermediateData.ToolUses {
			toolUses[i] = convertFunctionCallDTO{ID: tu.ID, Name: tu.Name, Args: tu.Args}
		}
		toolResponses := make([]convertFunctionResponseDTO, len(inv.IntermediateData.ToolResponses))
		for i, tr := range inv.IntermediateData.ToolResponses {
			toolResponses[i] = convertFunctionResponseDTO{ID: tr.ID, Name: tr.Name, Response: tr.Response}
		}
		dto.IntermediateData = &convertIntermediateDataDTO{ToolUses: toolUses, ToolResponses: toolResponses}
	}
	return dto
}

// sessionNameFromFilenameRe strips a trailing .json/.jsonl extension.
var sessionNameFromFilenameRe = regexp.MustCompile(`(?i)\.(jsonl?|json)$`)

// sessionNameFromFilename extracts a session name from a trace filename, stripping known prefixes.
func sessionNameFromFilename(filename string) string {
	base := sessionNameFromFilenameRe.ReplaceAllString(filename, "")
	for _, prefix := range []string{"trace_", "ego_"} {
		if strings.HasPrefix(base, prefix) {
			return base[len(prefix):]
		}
	}
	return ""
}

// convertHandler implements POST /api/convert for uploaded trace files, or for stored sessions named by id in a
// JSON body (see sessionrefs.go).
func convertHandler(sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		convertTraces(w, r, sessions)
	}
}

func convertTraces(w http.ResponseWriter, r *http.Request, sessions *SessionStore) {
	var inputs []traceInput
	var loadWarnings []string
	traceFormat := ""

	if isJSONBody(r) {
		var refs sessionRefs
		if err := decodeJSON(w, r, &refs); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		var msg string
		if inputs, msg = sessionTraceInputs(sessions, r, refs.SessionIDs); msg != "" {
			status := http.StatusBadRequest
			if strings.HasSuffix(msg, "not found") {
				status = http.StatusNotFound
			}
			writeEvaluateError(w, status, msg)
			return
		}
	} else {
		limitUpload(w, r)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
			return
		}

		traceFileHeaders := r.MultipartForm.File["trace_files"]
		if len(traceFileHeaders) == 0 {
			writeEvaluateError(w, http.StatusBadRequest, "No valid trace files provided")
			return
		}
		traceFormat = r.FormValue("trace_format")

		for _, fh := range traceFileHeaders {
			lower := strings.ToLower(fh.Filename)
			if !strings.HasSuffix(lower, ".json") && !strings.HasSuffix(lower, ".jsonl") {
				writeEvaluateError(w, http.StatusBadRequest,
					fmt.Sprintf("Invalid file extension for %s. Only .json and .jsonl files are allowed.", fh.Filename))
				return
			}
			content, err := readMultipartFile(fh, maxUploadFileSize)
			if err != nil {
				loadWarnings = append(loadWarnings, fmt.Sprintf("Failed to read trace file %q: %s", fh.Filename, err))
				continue
			}
			inputs = append(inputs, traceInput{Filename: fh.Filename, Content: content})
		}
	}

	var traces []*tracepkg.Trace
	traceToFilename := map[string]string{}
	for _, in := range inputs {
		parsed, err := loader.ParseFile(in.Filename, in.Content, traceFormat)
		if err != nil {
			loadWarnings = append(loadWarnings, fmt.Sprintf("Failed to load '%s': %s", in.Filename, err))
			continue
		}
		for _, t := range parsed {
			traceToFilename[t.TraceID] = in.Filename
		}
		traces = append(traces, parsed...)
	}

	if len(traces) == 0 {
		detail := "No traces found in uploaded files"
		if len(loadWarnings) > 0 {
			detail += ". Errors: " + strings.Join(loadWarnings, "; ")
		}
		writeEvaluateError(w, http.StatusBadRequest, detail)
		return
	}

	entries := make([]traceConversionEntryDTO, 0, len(traces))
	for _, tr := range traces {
		result := adk.ConvertTrace(tr)

		invocations := make([]convertInvocationDTO, len(result.Invocations))
		for i, inv := range result.Invocations {
			invocations[i] = toConvertInvocationDTO(inv)
		}

		meta := adk.ExtractTraceMetadata(tr, result.Invocations)
		entries = append(entries, traceConversionEntryDTO{
			TraceID:     tr.TraceID,
			Invocations: invocations,
			// append onto a non-nil empty slice so a nil result.Warnings still
			// marshals as JSON [] not null (matches the TS warnings: string[]
			// non-optional type; UI reads .length without optional chaining).
			Warnings: append([]string{}, result.Warnings...),
			Metadata: traceConversionMetadataDTO{
				AgentName:          meta.AgentName,
				Model:              meta.Model,
				StartTime:          meta.StartTime,
				UserInputPreview:   meta.UserInputPreview,
				FinalOutputPreview: meta.FinalOutputPreview,
				SessionName:        sessionNameFromFilename(traceToFilename[tr.TraceID]),
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  convertTracesDataDTO{Traces: entries},
		"error": nil,
	})
}
