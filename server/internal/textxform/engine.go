package textxform

import (
	"slices"
	"strings"
	"unicode/utf8"

	"xlyra/server/internal/textmatch"
)

// Options controls one Apply.
type Options struct {
	// Types are the declared types in priority order.
	Types []string
	// PropagateSameValue replaces every other exact occurrence of a replaced
	// value of at least PropagateMinLength characters.
	PropagateSameValue bool
	PropagateMinLength int
}

// Result is what Apply did to the texts.
type Result struct {
	Texts   []string
	Changed []bool
	// Applied are the spans replaced in each text, with placeholders.
	Applied [][]AppliedSpan
}

// AppliedSpan is one replaced range of the input text.
type AppliedSpan struct {
	textmatch.Span
	Placeholder string
	Propagated  bool
}

// Apply replaces the findings of each text with placeholders of session. It
// picks non-overlapping findings (longer first, then the earlier declared
// type, then the earlier start), numbers new values in the order of the texts
// and of their starts, which keeps numbers stable while a conversation grows,
// and then propagates replaced values to their other occurrences.
func Apply(session *Session, texts []string, findings [][]textmatch.Span, opts Options) Result {
	rank := make(map[string]int, len(opts.Types))
	for i, typ := range opts.Types {
		rank[typ] = i
	}
	rankOf := func(typ string) int {
		if r, ok := rank[typ]; ok {
			return r
		}
		return len(opts.Types)
	}
	for _, text := range texts {
		session.Occupy(text)
	}
	selected := make([][]AppliedSpan, len(texts))
	for i, text := range texts {
		var spans []textmatch.Span
		if i < len(findings) {
			spans = findings[i]
		}
		picked := SelectSpans(spans, rankOf, existingPlaceholders(text))
		for _, span := range picked {
			original := text[span.Start:span.End]
			token, ok := session.PlaceholderFor(span.Type, original, opts.PropagateSameValue)
			if !ok {
				token = session.Allocate(span.Type, original)
			}
			selected[i] = append(selected[i], AppliedSpan{Span: span, Placeholder: token})
		}
	}
	if opts.PropagateSameValue {
		propagate(texts, selected, rankOf, opts.PropagateMinLength)
	}
	result := Result{Texts: make([]string, len(texts)), Changed: make([]bool, len(texts)), Applied: selected}
	for i, text := range texts {
		if len(selected[i]) == 0 {
			result.Texts[i] = text
			continue
		}
		var b strings.Builder
		b.Grow(len(text))
		last := 0
		for _, span := range selected[i] {
			b.WriteString(text[last:span.Start])
			b.WriteString(span.Placeholder)
			last = span.End
			session.count(span.Type)
		}
		b.WriteString(text[last:])
		result.Texts[i] = b.String()
		result.Changed[i] = true
	}
	return result
}

// existingPlaceholders returns the ranges of placeholders already in text.
func existingPlaceholders(text string) [][2]int {
	if !strings.Contains(text, "[[") {
		return nil
	}
	var out [][2]int
	for _, loc := range PlaceholderPattern.FindAllStringIndex(text, -1) {
		out = append(out, [2]int{loc[0], loc[1]})
	}
	return out
}

// intervals is a set of non-overlapping ranges sorted by start, so their ends
// are sorted too.
type intervals [][2]int

func newIntervals(ranges [][2]int) intervals {
	out := append(intervals(nil), ranges...)
	slices.SortFunc(out, func(a, b [2]int) int { return a[0] - b[0] })
	return out
}

// overlaps reports whether [start, end) meets a range of the set: the first
// range that starts at or after start, or the one before it.
func (iv intervals) overlaps(start, end int) bool {
	i, _ := slices.BinarySearchFunc(iv, start, func(r [2]int, target int) int { return r[0] - target })
	if i < len(iv) && iv[i][0] < end {
		return true
	}
	return i > 0 && iv[i-1][1] > start
}

func (iv *intervals) insert(start, end int) {
	i, _ := slices.BinarySearchFunc(*iv, start, func(r [2]int, target int) int { return r[0] - target })
	*iv = slices.Insert(*iv, i, [2]int{start, end})
}

// SelectSpans keeps a set of non-overlapping spans: longer first, then the
// type declared earlier, then the earlier start; exact duplicates once. Spans
// touching a blocked range are dropped; blocked ranges must not overlap each
// other. The result is ordered by start.
func SelectSpans(spans []textmatch.Span, rankOf func(string) int, blocked [][2]int) []textmatch.Span {
	if len(spans) == 0 {
		return nil
	}
	blockedSet := newIntervals(blocked)
	candidates := make([]textmatch.Span, 0, len(spans))
	for _, span := range spans {
		if span.End <= span.Start || blockedSet.overlaps(span.Start, span.End) {
			continue
		}
		candidates = append(candidates, span)
	}
	slices.SortStableFunc(candidates, func(a, b textmatch.Span) int {
		if la, lb := a.End-a.Start, b.End-b.Start; la != lb {
			return lb - la
		}
		if ra, rb := rankOf(a.Type), rankOf(b.Type); ra != rb {
			return ra - rb
		}
		return a.Start - b.Start
	})
	var taken intervals
	var out []textmatch.Span
	for _, span := range candidates {
		if taken.overlaps(span.Start, span.End) {
			continue
		}
		taken.insert(span.Start, span.End)
		out = append(out, span)
	}
	textmatch.SortSpans(out)
	return out
}

// propagate adds, to every text, the other exact occurrences of the values
// replaced anywhere in the request.
func propagate(texts []string, selected [][]AppliedSpan, rankOf func(string) int, minLength int) {
	if minLength <= 0 {
		minLength = 2
	}
	type value struct {
		original string
		token    string
		typ      string
	}
	var values []value
	seen := map[string]bool{}
	for i, spans := range selected {
		for _, span := range spans {
			original := texts[i][span.Start:span.End]
			if seen[original] || utf8.RuneCountInString(original) < minLength {
				continue
			}
			seen[original] = true
			values = append(values, value{original: original, token: span.Placeholder, typ: span.Type})
		}
	}
	if len(values) == 0 {
		return
	}
	words := make([]string, len(values))
	for i, v := range values {
		words[i] = v.original
	}
	set := textmatch.NewLiteralSet(words, true)
	for i, text := range texts {
		matches := set.FindAll(text)
		if len(matches) == 0 {
			continue
		}
		blocked := existingPlaceholders(text)
		for _, span := range selected[i] {
			blocked = append(blocked, [2]int{span.Start, span.End})
		}
		spans := make([]textmatch.Span, 0, len(matches))
		for _, match := range matches {
			spans = append(spans, textmatch.Span{Start: match.Start, End: match.End, Type: values[match.Index].typ, Source: "propagated"})
		}
		picked := SelectSpans(spans, rankOf, blocked)
		if len(picked) == 0 {
			continue
		}
		tokens := map[string]string{}
		for _, v := range values {
			tokens[v.original] = v.token
		}
		for _, span := range picked {
			selected[i] = append(selected[i], AppliedSpan{Span: span, Placeholder: tokens[text[span.Start:span.End]], Propagated: true})
		}
		slices.SortFunc(selected[i], func(a, b AppliedSpan) int { return a.Start - b.Start })
	}
}
