package decision

import (
	"encoding/json"
	"unicode/utf8"
)

// cutMark ends a string fitState has shortened, so the model can tell the text stopped there.
const cutMark = " […cut]"

// fitState returns state shortened to at most max bytes of JSON, for a decision model with a small context. It
// halves the longest string over and over, so the long fields (a tool result, a long reply) give way first and the
// short ones (the user's question) stay whole. A state whose structure alone is over max comes back as shortened as
// it can get; the endpoint then answers for itself.
func fitState(state map[string]any, max int) map[string]any {
	raw, err := json.Marshal(state)
	if err != nil || len(raw) <= max {
		return state
	}
	var generic map[string]any
	if json.Unmarshal(raw, &generic) != nil {
		return state
	}
	for size := len(raw); size > max; {
		longest := longestString(generic)
		if longest == nil || len(*longest.s) <= len(cutMark)+8 {
			break
		}
		keep := len(*longest.s) / 2
		if over := size - max; over < keep { // no need to cut more than what is over
			keep = len(*longest.s) - over - len(cutMark)
			if keep < len(*longest.s)/2 {
				keep = len(*longest.s) / 2
			}
		}
		for keep > 0 && !utf8.RuneStart((*longest.s)[keep]) {
			keep--
		}
		*longest.s = (*longest.s)[:keep] + cutMark
		longest.set(*longest.s)
		next, err := json.Marshal(generic)
		if err != nil {
			break
		}
		size = len(next)
	}
	return generic
}

// leaf is a string inside a decoded JSON value, with a way to write it back.
type leaf struct {
	s   *string
	set func(string)
}

func longestString(v any) *leaf {
	var best *leaf
	var walk func(v any)
	consider := func(s string, set func(string)) {
		if best == nil || len(s) > len(*best.s) {
			cp := s
			best = &leaf{s: &cp, set: set}
		}
	}
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok {
					k := k
					consider(s, func(n string) { x[k] = n })
				} else {
					walk(e)
				}
			}
		case []any:
			for i, e := range x {
				if s, ok := e.(string); ok {
					i := i
					consider(s, func(n string) { x[i] = n })
				} else {
					walk(e)
				}
			}
		}
	}
	walk(v)
	return best
}
