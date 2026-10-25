package mcptools

import (
	"fmt"
	"strings"
	"time"
)

// The get_ego_* tools answer in text the agent reads at a glance (key: value lines and small tables), with the
// structured form beside it for a client that wants fields. The text names the next call at its foot, so an agent
// that sees a bad number knows where the detail is.

const egoNext = "next: get_ego_tools (every tool), get_ego_errors (what failed and what it said), get_session_spans with a session and errors_only (one turn in full)"

func pctOf(x float64) string { return fmt.Sprintf("%d%%", int(x*100+0.5)) }

func shortN(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func fmtMs(ms float64) string {
	switch {
	case ms <= 0:
		return "-"
	case ms < 1000:
		return fmt.Sprintf("%dms", int(ms+0.5))
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	s := int(ms/1000 + 0.5)
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

func windowName(h int) string {
	if h > 48 && h%24 == 0 {
		return fmt.Sprintf("last %dd", h/24)
	}
	return fmt.Sprintf("last %dh", h)
}

func subject(agent, tenant string) string {
	if tenant == "" {
		return agent
	}
	return agent + " (" + tenant + ")"
}

func stamp(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.UTC().Format("01-02 15:04Z")
}

func failRate(failed, calls int) string {
	if calls == 0 {
		return "-"
	}
	return fmt.Sprintf("%s (%d/%d)", pctOf(float64(failed)/float64(calls)), failed, calls)
}

func scoreText(s *float64) string {
	if s == nil {
		return "-"
	}
	return pctOf(*s)
}

// RenderEgoScore writes the score rows of get_ego_score. By says how the rows were cut.
func RenderEgoScore(rows []TurnScore, q ActivityQuery) string {
	var b strings.Builder
	if len(rows) == 0 {
		who := "any agent"
		if q.Agent != "" {
			who = q.Agent
		}
		fmt.Fprintf(&b, "no turns for %s in the %s", who, windowName(q.Hours))
		if q.Session != "" {
			fmt.Fprintf(&b, " in session %s", q.Session)
		}
		b.WriteString(".\n")
		return b.String()
	}
	switch q.By {
	case ScoreBySession, ScoreByHour, ScoreByDay:
		cut := q.By
		order := "oldest first"
		if q.By == ScoreBySession {
			order = "newest first"
		}
		agent, tenant := "", "\x00"
		for _, r := range rows {
			if r.Agent != agent || r.Tenant != tenant {
				if agent != "" {
					b.WriteString("\n")
				}
				agent, tenant = r.Agent, r.Tenant
				fmt.Fprintf(&b, "## ego by %s · %s · %s, %s\n", cut, subject(r.Agent, r.Tenant), windowName(r.Hours), order)
				first := "session"
				if q.By != ScoreBySession {
					first = "period"
				}
				fmt.Fprintf(&b, "| %s | from | to | turns | score | tool errors | tokens in/out | worst tool |\n|---|---|---|---|---|---|---|---|\n", first)
			}
			id := r.Session
			if q.By != ScoreBySession {
				id = stamp(r.From)
			}
			worst := "-"
			if len(r.FailingTools) > 0 {
				w := r.FailingTools[0]
				worst = fmt.Sprintf("%s %d/%d", w.Tool, w.Failed, w.Calls)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s | %s/%s | %s |\n", id, stamp(r.From), stamp(r.To), r.Turns, scoreText(r.Score),
				failRate(r.FailedToolCalls, r.ToolCalls), shortN(r.InputTokens), shortN(r.OutputTokens), worst)
		}
		b.WriteString("\n" + egoNext + "\n")
		return b.String()
	}
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## ego · %s · %s", subject(r.Agent, r.Tenant), windowName(r.Hours))
		if q.Session != "" {
			fmt.Fprintf(&b, " · session %s", q.Session)
		}
		b.WriteString("\n")
		ok := 0
		if r.Score != nil {
			ok = int(*r.Score*float64(r.Turns) + 0.5)
		}
		fmt.Fprintf(&b, "score: %s (%d of %d turns ended without a failure)\n", scoreText(r.Score), ok, r.Turns)
		fmt.Fprintf(&b, "tool_error_rate: %s of tool calls failed\n", failRate(r.FailedToolCalls, r.ToolCalls))
		per := 0.0
		if r.Turns > 0 {
			per = float64(r.ModelRequests) / float64(r.Turns)
		}
		fmt.Fprintf(&b, "model_requests: %d (%.1f per turn)\n", r.ModelRequests, per)
		fmt.Fprintf(&b, "tokens: input %s (%s from cache), output %s\n", shortN(r.InputTokens), pctOf(r.CachedShare), shortN(r.OutputTokens))
		fmt.Fprintf(&b, "turn_time: median %s, p95 %s; awake %s of the window\n", fmtMs(float64(r.MedianTurnMs)), fmtMs(float64(r.P95TurnMs)), pctOf(r.Awake))
		if len(r.FailingTools) == 0 {
			b.WriteString("failing_tools: none\n")
		} else {
			b.WriteString("failing_tools:\n")
			for _, t := range r.FailingTools {
				fmt.Fprintf(&b, "- %s: %s failed, avg %s\n", toolLabel(t), failRate(t.Failed, t.Calls), fmtMs(t.AvgMs))
			}
		}
	}
	b.WriteString("\n" + egoNext + "\n")
	return b.String()
}

func toolLabel(t ToolRecord) string {
	if t.Server == "" {
		return t.Tool
	}
	return t.Server + "/" + t.Tool
}

// RenderEgoTools writes get_ego_tools: one table of tools per agent.
func RenderEgoTools(rows []EgoTools, q ActivityQuery) string {
	if len(rows) == 0 {
		return "no turns in the " + windowName(q.Hours) + ".\n"
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## ego tools · %s · %s, most failures first\n", subject(r.Agent, r.Tenant), windowName(r.Hours))
		if len(r.Tools) == 0 {
			b.WriteString("no tool calls\n")
			continue
		}
		b.WriteString("| tool | calls | failed | failure rate | avg |\n|---|---|---|---|---|\n")
		for _, t := range r.Tools {
			failed, rate := "-", "-"
			if t.Failed > 0 {
				failed, rate = fmt.Sprint(t.Failed), pctOf(t.Failure)
			}
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", toolLabel(t), t.Calls, failed, rate, fmtMs(t.AvgMs))
		}
	}
	b.WriteString("\nnext: get_ego_errors (what the failures said), get_session_spans with errors_only (one turn in full)\n")
	return b.String()
}

// RenderEgoErrors writes get_ego_errors: the failures of each agent, the most frequent first.
func RenderEgoErrors(rows []EgoErrors, q ActivityQuery) string {
	if len(rows) == 0 {
		return "no turns in the " + windowName(q.Hours) + ".\n"
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## ego errors · %s · %s, most frequent first\n", subject(r.Agent, r.Tenant), windowName(r.Hours))
		if len(r.Errors) == 0 {
			b.WriteString("no failed tool calls\n")
			continue
		}
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %d× %s: %q\n  first %s, last %s, latest in session %s\n", e.Count, toolLabel(ToolRecord{Tool: e.Tool, Server: e.Server}), e.Message, stamp(e.FirstAt), stamp(e.LastAt), e.Session)
		}
	}
	b.WriteString("\nnext: get_session_spans with that session and errors_only (the failed turn in full); then decide what to do differently\n")
	return b.String()
}
