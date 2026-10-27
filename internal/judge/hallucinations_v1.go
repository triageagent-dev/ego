package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/triageagent-dev/ego/internal/adk"
	"github.com/triageagent-dev/ego/internal/eval"
)

const hallucinationsSegmenterPrompt = "You are a helpful and harmless AI assistant. You will be provided with a model-generated response.\nYour task is to segment the provided response sentence by sentence so that we could analyze each sentence in the future.\n\n**Instructions:**\n1. Overall, you should decompose the whole provided response into individual sentences. You should make sure the output covers ALL the sentences in the provided response block.\n2. You should COPY each sentence as it is, WORD BY WORD. DO NOT modify the sentence or the surrounding punctuation.\n3. If there are bullet points in the response, you should segment each bullet point into DIFFERENT sentences. If one bullet point has sub bullet points, you should further decompose sub bullet points into DIFFERENT sentences.\nFor example, if there are responses like \"it has three criteria: * aaa. * bbb. * ccc\", you should segment them into FOUR sentences: \"it has three criteria\", \"aaa\", \"bbb\", \"ccc\". Bullet points could start with numbers (1/2/3/etc) or symbols like \"*\", \"-\" etc.\n4. When encountering tables, you should include the whole table in ONE sentence output.\n5. Each sentence should be meaningful to further analyze on. DO NOT ONLY put symbols themselves into a sentence.\n6. You should ONLY output segmented sentences in the provided response. DO NOT make up any new sentences.\n\n**Input Format:**\n\nThe input will be the model-generated response:\n* **Response:** The model-generated response to be analyzed.\n\n**Output Format:**\n\nFor each decomposed sentence, wrap them with <sentence> and </sentence> like the following:\n<sentence>...</sentence>\n<sentence>...</sentence>\n\n**Example:**\n\n**Input:**\n\n**Response Begin**\nThere are three kinds of fruits:\n1. Apples are red.\n2. Bananas are green.\n3. Pears are purple.\n\nFor prices:\n* Bananas are cheaper than apples.\n\nEnjoy your fruit!\n**Response End**\n\n**Output:**\n<sentence>There are three kinds of fruits:</sentence>\n<sentence>1. Apples are red.</sentence>\n<sentence>2. Bananas are green.</sentence>\n<sentence>3. Pears are purple.</sentence>\n<sentence>For prices:</sentence>\n<sentence>* Bananas are cheaper than apples.</sentence>\n<sentence>Enjoy your fruit!</sentence>\n\n**Now, given the following response, please segment the response into sentences:**\n\n**Input:**\n\n**Response Begin**\n%s\n**Response End**\n\n**Your Sentence Segmentation Output:**"

const hallucinationsValidatorPrompt = "You are a helpful and harmless AI assistant. You will be provided with a textual context and sentences from a model-generated response.\nYour task is to analyze sentence by sentence and classify each sentence according to its relationship with the provided context.\n\n**Instructions:**\n\n1. **Read the textual context carefully.**\n2. **For each sentence, assign one of the following labels:**\n    * **`supported`**: The sentence is entailed by the given context. Provide a supporting excerpt from the context. The supporting except must *fully* entail the sentence.\n    * **`unsupported`**: The sentence is not entailed by the given context. No excerpt is needed for this label.\n    * **`contradictory`**: The sentence is falsified by the given context. Provide a contradicting excerpt from the context.\n    * **`disputed`**: The given context contains both supporting and contradicting information. Provide both supporting and contradicting excerpt from the context.\n    * **`not_applicable`**: The sentence does not require factual attribution (e.g., opinions, planning steps, greetings, questions, disclaimers, mathematical calculation).\n3. **For each label, provide a short rationale explaining your decision.** The rationale should be separate from the excerpt.\n4. **Be very strict with your `supported`, `contradictory` and `disputed` decisions.** Unless you can find straightforward, indisputable evidence excepts *in the context* that a sentence is `supported`, `contradictory` or `disputed`, consider it `unsupported`.  You should not employ world knowledge unless it is truly trivial.\n5. \"tool_outputs\" blocks contain code execution results of the \"tool_code\" blocks immediately above them. If any sentence is based on \"tool_outputs\" results, first analyze if the corresponding \"tool_code\" is supported and if the results are error-free. Only if the \"tool_code\" block is supported, you can treat code execution results as correct.\n6. If you need to cite multiple supporting excerpts, simply concatenate them. Excerpt could be summary from the context if it is too long.\n\n**Input Format:**\n\nThe input will consist of two parts, clearly separated:\n\n* **Context:**  The textual context used to generate the response.\n* **Sentences:** The sentences from the model-generated response to be analyzed. Each sentence will be wrapped in <sentence>...</sentence>.\n\n**Output Format:**\n\nFor each sentence, output a block of text with the following fields:\n\n* sentence: The sentence being analyzed. Please directly copy the sentence which is provided.\n* label: One of `supported`, `unsupported`, `contradictory`, `disputed` or `not_applicable`.\n* rationale: A brief explanation for the assessment\n* supporting_excerpt: A relevant excerpt from the context that supports the sentence. Only required for `supported` and `disputed` labels.\n* contradicting_excerpt: A relevant excerpt from the context that contradicts with the sentence. Only required for `contradictory` and `disputed` labels.\n\n**Example:**\n\n**Input:**\n\n**Context Begin**\nApples are red fruits. Bananas are yellow fruits. Pears are purple fruits. Pears are blue fruits.\n**Context End**\n\n**Sentences Begin**\n<sentence>Apples are red.</sentence>\n<sentence>Bananas are green.</sentence>\n<sentence>Pears are purple.</sentence>\n<sentence>Bananas are cheaper than apples.</sentence>\n<sentence>Enjoy your fruit!</sentence>\n**Sentences End**\n\n**Output:**\nsentence: Apples are red.\nlabel: supported\nrationale: The context explicitly states that apples are red.\nsupporting_excerpt: Apples are red fruits.\ncontradicting_excerpt: null\n\nsentence: Bananas are green.\nlabel: contradictory\nrationale: The context states that bananas are yellow, not green.\nsupporting_excerpt: null\ncontradicting_excerpt: Bananas are yellow fruits.\n\nsentence: Pears are purple.\nlabel: disputed\nrationale: The context states that pears are purple but it also states that pears are blue.\nsupporting_excerpt: Pears are purple fruits\ncontradicting_excerpt: Pears are blue fruits\n\nsentence: Bananas are cheaper than apples.\nlabel: unsupported\nrationale: The context does not mention the price of bananas or apples.\nsupporting_excerpt: null\ncontradicting_excerpt: null\n\nsentence: Enjoy your fruit!\nlabel: not_applicable\nrationale: This is a general expression and does not require factual attribution.\nsupporting_excerpt: null\ncontradicting_excerpt: null\n\n**Now, please analyze the following context and sentences:**\n\n**Input:**\n\n**Context Begin**\n%s\n**Context End**\n\n**Sentences Begin**\n%s\n**Sentences End**\n\n**Output:**"

// Go's RE2 has no lookahead, so the terminator is captured as an ordinary (consuming) group instead;
// parseHallucinationValidationResults compensates by only advancing its search past the *start* of that
// captured group on each iteration (see below), not past its end, to reproduce the same zero-width effect.
var (
	hallucinationSentenceRe   = regexp.MustCompile(`(?s)<sentence>(.*?)</sentence>`)
	hallucinationValidationRe = regexp.MustCompile(`(?is)sentence:(.*?)\nlabel:(.*?)\nrationale:(.*?)\nsupporting_excerpt:(.*?)\ncontradicting_excerpt:(.*?)(\nsentence:|$)`)
)

var (
	hallucinationPositiveLabels = map[string]bool{"supported": true, "not_applicable": true}
	hallucinationNegativeLabels = map[string]bool{"unsupported": true, "contradictory": true, "disputed": true}
)

// Caps on what the judge is shown of the tools: one result, and all of a turn's results together.
const (
	maxToolOutputChars  = 6000
	maxToolOutputsTotal = 24000
)

// buildHallucinationContext is what the response may be checked against: the user's message, the tools the agent
// used and what they returned. A sentence that rests on a tool result is supported by it; without the results every
// fact an agent looked up reads as invented, so the judge failed correct answers as often as wrong ones. The
// tool_code and tool_outputs blocks are the ones the validator prompt's fifth rule speaks of.
func buildHallucinationContext(inv adk.Invocation) string {
	var uses []adk.FunctionCall
	var responses []adk.FunctionResponse
	if inv.IntermediateData != nil {
		uses, responses = inv.IntermediateData.ToolUses, inv.IntermediateData.ToolResponses
	}
	var b strings.Builder
	b.WriteString("Developer instructions:\n\n\n")
	b.WriteString("User prompt:\n")
	b.WriteString(plainText(inv.UserContent.Text()))
	b.WriteString("\n\nTool definitions:\n")
	if len(uses) == 0 {
		b.WriteString("Agent has no tools.\n")
		return b.String()
	}
	seen := map[string]bool{}
	for _, u := range uses {
		if !seen[u.Name] {
			seen[u.Name] = true
			b.WriteString("- " + oneLine(u.Name) + "\n")
		}
	}
	budget := maxToolOutputsTotal
	for i, u := range uses {
		args, _ := json.Marshal(u.Args)
		fmt.Fprintf(&b, "\n```tool_code\n%s(%s)\n```\n", oneLine(u.Name), plainText(string(args)))
		out := ""
		if r, ok := toolResponseFor(u, i, responses); ok {
			raw, _ := json.Marshal(r.Response)
			out = string(raw)
		}
		if len(out) > maxToolOutputChars {
			out = clipUTF8(out, maxToolOutputChars) + " …"
		}
		if len(out) > budget {
			out = clipUTF8(out, budget) + " …"
		}
		budget = max(budget-len(out), 0)
		fmt.Fprintf(&b, "```tool_outputs\n%s\n```\n", plainText(out))
	}
	return b.String()
}

// contextHeading matches the lines of the context that the validator prompt relies on to tell the user's words, the
// tool definitions and the tool results apart.
var contextHeading = regexp.MustCompile(`(?m)^(Developer instructions:|User prompt:|Tool definitions:)`)

// plainText keeps text that came from outside from forging the structure of the context: a closing fence would end
// a tool_code or tool_outputs block and let what follows pass for the prompt's own text, and a heading at the start
// of a line would open a section. A fence becomes three quotes and a heading is marked as quoted.
func plainText(s string) string {
	return contextHeading.ReplaceAllString(strings.ReplaceAll(s, "```", "\x27\x27\x27"), "· $1")
}

// oneLine is plainText for a name, which has no business spanning lines.
func oneLine(s string) string {
	return plainText(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

// clipUTF8 cuts s to at most n bytes without splitting a multi-byte character.
func clipUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// toolResponseFor pairs a call with its result by id, else by position.
func toolResponseFor(u adk.FunctionCall, i int, responses []adk.FunctionResponse) (adk.FunctionResponse, bool) {
	if u.ID != "" {
		for _, r := range responses {
			if r.ID == u.ID {
				return r, true
			}
		}
	}
	if i < len(responses) && (responses[i].Name == "" || responses[i].Name == u.Name) {
		return responses[i], true
	}
	return adk.FunctionResponse{}, false
}

func parseHallucinationSentences(response string) []string {
	matches := hallucinationSentenceRe.FindAllStringSubmatch(response, -1)
	sentences := make([]string, 0, len(matches))
	for _, m := range matches {
		sentences = append(sentences, m[1])
	}
	return sentences
}

// The rationale and excerpt fields are captured so callers can
// surface "why" a sentence was judged supported/unsupported/ etc. - see HallucinationsV1's Details["sentences"]
// and ui/src/components/inspector/MetricsComparisonSection.tsx.
type hallucinationValidationResult struct {
	Sentence             string
	Label                string
	Rationale            string
	SupportingExcerpt    string
	ContradictingExcerpt string
}

// nullExcerpt returns "" for the literal "null" the validator prompt asks
// for when an excerpt doesn't apply, otherwise the trimmed excerpt text.
func nullExcerpt(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "null") {
		return ""
	}
	return s
}

func parseHallucinationValidationResults(response string) []hallucinationValidationResult {
	text := strings.TrimSpace(response)
	var results []hallucinationValidationResult
	pos := 0
	for pos <= len(text) {
		loc := hallucinationValidationRe.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		sentence := text[pos+loc[2] : pos+loc[3]]
		label := text[pos+loc[4] : pos+loc[5]]
		rationale := text[pos+loc[6] : pos+loc[7]]
		supportingExcerpt := text[pos+loc[8] : pos+loc[9]]
		contradictingExcerpt := text[pos+loc[10] : pos+loc[11]]
		results = append(results, hallucinationValidationResult{
			Sentence:             strings.TrimSpace(sentence),
			Label:                strings.ToLower(strings.TrimSpace(label)),
			Rationale:            strings.TrimSpace(rationale),
			SupportingExcerpt:    nullExcerpt(supportingExcerpt),
			ContradictingExcerpt: nullExcerpt(contradictingExcerpt),
		})

		advance := loc[12] // start of the terminator group, relative to pos
		if advance <= 0 {
			// The terminator matched a zero-width "$" (end of input);
			// advance past the whole match instead to guarantee forward
			// progress and avoid an infinite loop.
			advance = loc[1]
			if advance <= 0 {
				break
			}
		}
		pos += advance
	}
	return results
}

// evaluateHallucinationResponse runs the segmenter then validator prompts for one natural-language response,
// returning the accuracy score (the fraction of sentences labeled supported/not_applicable), or nil if nothing
// could be scored, plus the per-sentence classifications (label + rationale + supporting/contradicting excerpt)
// so callers can explain "why" the score came out the way it did.
func evaluateHallucinationResponse(ctx context.Context, model Model, nlResponse, contextStr string) (*float64, []hallucinationValidationResult, error) {
	segResp, err := model.Generate(ctx, fmt.Sprintf(hallucinationsSegmenterPrompt, defang(nlResponse)))
	if err != nil {
		return nil, nil, fmt.Errorf("segmenter: %w", err)
	}
	sentences := parseHallucinationSentences(segResp)
	if len(sentences) == 0 {
		return nil, nil, nil
	}

	var sentencesBuilder strings.Builder
	for i, s := range sentences {
		if i > 0 {
			sentencesBuilder.WriteString("\n")
		}
		sentencesBuilder.WriteString("<sentence>")
		sentencesBuilder.WriteString(defang(s))
		sentencesBuilder.WriteString("</sentence>")
	}

	valResp, err := model.Generate(ctx, fmt.Sprintf(hallucinationsValidatorPrompt, defang(contextStr), sentencesBuilder.String()))
	if err != nil {
		return nil, nil, fmt.Errorf("validator: %w", err)
	}
	results := parseHallucinationValidationResults(valResp)

	var scores []float64
	for _, r := range results {
		switch {
		case hallucinationPositiveLabels[r.Label]:
			scores = append(scores, 1)
		case hallucinationNegativeLabels[r.Label]:
			scores = append(scores, 0)
		}
	}
	if len(scores) == 0 {
		return nil, results, nil
	}
	var sum float64
	for _, s := range scores {
		sum += s
	}
	mean := sum / float64(len(scores))
	return &mean, results, nil
}

// HallucinationsV1 scores each actual invocation's final response for hallucinations (unsupported/contradictory
// claims against the invocation's own context) via a two-stage judge-model pipeline, then averages the
// per-invocation scores.
func HallucinationsV1(ctx context.Context, model Model, actual []adk.Invocation, threshold float64) (eval.Result, error) {
	const name = "hallucinations_v1"

	perInvocation := make([]float64, len(actual))
	// perInvocationDetail is parallel to perInvocation/actual (same index
	// convention as trajectory.go's Details["comparisons"]) and matches
	// ui/src/lib/types.ts's PersistedHallucinationInvocation/
	// HallucinationSentence shape exactly (snake_case sentence fields) so
	// the same rendering code works for both the live /api/evaluate
	// response and the persisted Runs details blob. Each entry
	// explains "why" that invocation's score came out the way it did: the
	// judge model's sentence-by-sentence supported/unsupported/
	// contradictory/disputed/not_applicable classification, rationale,
	// and any supporting/contradicting excerpt it cited.
	perInvocationDetail := make([]map[string]any, len(actual))
	var total float64
	var numScored int
	for i, inv := range actual {
		invocationID := inv.InvocationID
		text := inv.FinalResponse.Text()
		if text == "" {
			perInvocationDetail[i] = map[string]any{
				"invocation_id": invocationID,
				"score":         nil,
				"sentences":     []map[string]any{},
			}
			continue
		}
		score, results, err := evaluateHallucinationResponse(ctx, model, text, buildHallucinationContext(inv))
		if err != nil {
			return eval.Result{}, fmt.Errorf("invocation %d: %w", i, err)
		}
		sentences := make([]map[string]any, len(results))
		for j, r := range results {
			sentences[j] = map[string]any{
				"sentence":              r.Sentence,
				"label":                 r.Label,
				"rationale":             r.Rationale,
				"supporting_excerpt":    r.SupportingExcerpt,
				"contradicting_excerpt": r.ContradictingExcerpt,
			}
		}
		var scoreVal any
		if score != nil {
			scoreVal = *score
			perInvocation[i] = *score
			total += *score
			numScored++
		}
		perInvocationDetail[i] = map[string]any{
			"invocation_id": invocationID,
			"score":         scoreVal,
			"sentences":     sentences,
		}
	}

	if numScored == 0 {
		return eval.Result{MetricName: name, Status: eval.StatusNotEvaluated}, nil
	}
	overall := total / float64(numScored)
	status := eval.StatusFailed
	if overall >= threshold {
		status = eval.StatusPassed
	}
	return eval.Result{
		MetricName:          name,
		Score:               overall,
		Status:              status,
		PerInvocationScores: perInvocation,
		Details:             map[string]any{"per_invocation": perInvocationDetail},
	}, nil
}
