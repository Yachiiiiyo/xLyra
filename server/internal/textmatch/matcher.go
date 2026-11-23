// Package textmatch finds typed spans in text with rules, dictionaries and an
// allowlist compiled once and shared by every request. It is plain Go: linear
// time RE2 expressions and an Aho-Corasick word set.
package textmatch

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Finding sources.
const (
	SourceRule       = "rule"
	SourceDictionary = "dictionary"
)

// Boundaries a rule can require around its match.
const (
	BoundaryNone  = "none"
	BoundaryDigit = "digit"
	BoundaryAlnum = "alnum"
	BoundaryWord  = "word"
)

const (
	defaultContextWindow = 20
	// MaxDictionaryEntries bounds the words of all dictionaries of one matcher.
	MaxDictionaryEntries = 20000
	// MaxDictionaryWordBytes bounds one dictionary or allowlist line.
	MaxDictionaryWordBytes = 256
)

// Span is one finding at [Start, End) UTF-8 bytes of a text.
type Span struct {
	Start  int
	End    int
	Type   string
	Source string
	Score  float64
}

// RuleSpec is one compiled rule; see the gatewayPipeline manifest.
type RuleSpec struct {
	Type          string
	Pattern       string
	Group         int
	Boundary      string
	Validate      string
	MinEntropy    float64
	ContextAny    []string
	ContextWindow int
}

// DictionarySpec is the text of one dictionary input: a word per line, and
// word<TAB>TYPE to report another declared type.
type DictionarySpec struct {
	Type          string
	CaseSensitive bool
	Text          string
}

// Spec is everything a matcher is compiled from.
type Spec struct {
	// Types lists the declared types in priority order.
	Types []string
	// Disabled types are dropped from every source.
	Disabled               map[string]bool
	Rules                  []RuleSpec
	Dictionaries           []DictionarySpec
	Allowlist              string
	AllowlistCaseSensitive bool
}

type compiledRule struct {
	spec    RuleSpec
	re      *regexp.Regexp
	context []string
}

type dictionaryWord struct {
	typ string
	// edges record whether the word begins and ends with an ASCII letter or
	// digit, which then may not touch another one.
	leftWord  bool
	rightWord bool
}

type compiledDictionary struct {
	set   *LiteralSet
	words []dictionaryWord
}

// Matcher is immutable and safe for concurrent use.
type Matcher struct {
	types        []string
	rank         map[string]int
	disabled     map[string]bool
	rules        []compiledRule
	dictionaries []compiledDictionary
	allow        map[string]struct{}
	allowFold    bool
}

// Compile builds a matcher. Rule patterns and types were checked with the
// manifest; dictionary lines come from the binding and are checked here.
func Compile(spec Spec) (*Matcher, error) {
	m := &Matcher{
		types:    append([]string(nil), spec.Types...),
		rank:     make(map[string]int, len(spec.Types)),
		disabled: map[string]bool{},
		allow:    map[string]struct{}{},
	}
	for i, typ := range spec.Types {
		m.rank[typ] = i
	}
	for typ, off := range spec.Disabled {
		if off {
			m.disabled[typ] = true
		}
	}
	for i, rule := range spec.Rules {
		if _, ok := m.rank[rule.Type]; !ok {
			return nil, fmt.Errorf("rules[%d]: type %q is not declared", i, rule.Type)
		}
		if m.disabled[rule.Type] {
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", i, err)
		}
		if rule.Group < 0 || rule.Group > re.NumSubexp() {
			return nil, fmt.Errorf("rules[%d]: group %d is out of range", i, rule.Group)
		}
		if rule.ContextWindow <= 0 {
			rule.ContextWindow = defaultContextWindow
		}
		context := make([]string, 0, len(rule.ContextAny))
		for _, word := range rule.ContextAny {
			if word = strings.ToLower(strings.TrimSpace(word)); word != "" {
				context = append(context, word)
			}
		}
		m.rules = append(m.rules, compiledRule{spec: rule, re: re, context: context})
	}
	entries := 0
	for i, dict := range spec.Dictionaries {
		if _, ok := m.rank[dict.Type]; !ok {
			return nil, fmt.Errorf("dictionaries[%d]: type %q is not declared", i, dict.Type)
		}
		var words []string
		var meta []dictionaryWord
		for n, line := range splitLines(dict.Text) {
			word, typ := line, dict.Type
			if left, right, ok := strings.Cut(line, "\t"); ok {
				word = strings.TrimSpace(left)
				typ = strings.TrimSpace(right)
				if _, declared := m.rank[typ]; !declared {
					return nil, fmt.Errorf("dictionary line %d: type %q is not declared", n+1, typ)
				}
			}
			if word == "" {
				continue
			}
			if len(word) > MaxDictionaryWordBytes {
				return nil, fmt.Errorf("dictionary line %d is longer than %d bytes", n+1, MaxDictionaryWordBytes)
			}
			if m.disabled[typ] {
				continue
			}
			first, _ := utf8.DecodeRuneInString(word)
			last, _ := utf8.DecodeLastRuneInString(word)
			words = append(words, word)
			meta = append(meta, dictionaryWord{typ: typ, leftWord: asciiWordRune(first), rightWord: asciiWordRune(last)})
		}
		entries += len(words)
		if entries > MaxDictionaryEntries {
			return nil, fmt.Errorf("dictionaries hold more than %d words", MaxDictionaryEntries)
		}
		if len(words) == 0 {
			continue
		}
		m.dictionaries = append(m.dictionaries, compiledDictionary{set: NewLiteralSet(words, dict.CaseSensitive), words: meta})
	}
	m.allowFold = !spec.AllowlistCaseSensitive
	for n, line := range splitLines(spec.Allowlist) {
		if len(line) > MaxDictionaryWordBytes {
			return nil, fmt.Errorf("allowlist line %d is longer than %d bytes", n+1, MaxDictionaryWordBytes)
		}
		if line == "" {
			continue
		}
		m.allow[m.allowKey(line)] = struct{}{}
	}
	return m, nil
}

// splitLines returns the trimmed lines of a textarea value. A tab inside a
// line is kept for the word<TAB>TYPE form.
func splitLines(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.Trim(line, " \r　")
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
		}
	}
	return lines
}

func (m *Matcher) allowKey(value string) string {
	if m.allowFold {
		return strings.ToLower(value)
	}
	return value
}

// Types returns the declared types in priority order.
func (m *Matcher) Types() []string {
	if m == nil {
		return nil
	}
	return m.types
}

// TypeRank is the position of a declared type, lower first; unknown types last.
func (m *Matcher) TypeRank(typ string) int {
	if m == nil {
		return 0
	}
	if rank, ok := m.rank[typ]; ok {
		return rank
	}
	return len(m.types)
}

// Declared reports whether typ is a declared type.
func (m *Matcher) Declared(typ string) bool {
	if m == nil {
		return false
	}
	_, ok := m.rank[typ]
	return ok
}

// Enabled reports whether findings of typ are kept.
func (m *Matcher) Enabled(typ string) bool {
	return m.Declared(typ) && !m.disabled[typ]
}

// Allowed reports whether value is on the allowlist and must not be reported.
func (m *Matcher) Allowed(value string) bool {
	if m == nil || len(m.allow) == 0 {
		return false
	}
	_, ok := m.allow[m.allowKey(value)]
	return ok
}

// Keep reports whether a finding of typ over value survives the type switches
// and the allowlist. Detector findings go through it as well.
func (m *Matcher) Keep(typ, value string) bool {
	return m.Enabled(typ) && value != "" && !m.Allowed(value)
}

// Match returns the rule and dictionary findings of text, in no particular order.
func (m *Matcher) Match(text string) []Span {
	if m == nil || text == "" {
		return nil
	}
	var out []Span
	for _, rule := range m.rules {
		for _, idx := range rule.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := idx[2*rule.spec.Group], idx[2*rule.spec.Group+1]
			if start < 0 || end <= start {
				continue
			}
			value := text[start:end]
			if !checkBoundary(text, start, end, rule.spec.Boundary) {
				continue
			}
			if !Validate(rule.spec.Validate, value) {
				continue
			}
			if rule.spec.MinEntropy > 0 && Entropy(value) < rule.spec.MinEntropy {
				continue
			}
			if len(rule.context) > 0 && !contextHas(text, start, end, rule.spec.ContextWindow, rule.context) {
				continue
			}
			if !m.Keep(rule.spec.Type, value) {
				continue
			}
			out = append(out, Span{Start: start, End: end, Type: rule.spec.Type, Source: SourceRule})
		}
	}
	for _, dict := range m.dictionaries {
		for _, match := range dict.set.FindAll(text) {
			word := dict.words[match.Index]
			if word.leftWord && asciiWordRune(runeBefore(text, match.Start)) {
				continue
			}
			if word.rightWord && asciiWordRune(runeAfter(text, match.End)) {
				continue
			}
			if !m.Keep(word.typ, text[match.Start:match.End]) {
				continue
			}
			out = append(out, Span{Start: match.Start, End: match.End, Type: word.typ, Source: SourceDictionary})
		}
	}
	return out
}

// SortSpans orders spans by start, then by end.
func SortSpans(spans []Span) {
	slices.SortFunc(spans, func(a, b Span) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return a.End - b.End
	})
}

const noRune = rune(-1)

func runeBefore(text string, offset int) rune {
	if offset <= 0 {
		return noRune
	}
	r, _ := utf8.DecodeLastRuneInString(text[:offset])
	return r
}

func runeAfter(text string, offset int) rune {
	if offset >= len(text) {
		return noRune
	}
	r, _ := utf8.DecodeRuneInString(text[offset:])
	return r
}

func asciiWordRune(r rune) bool {
	return r < utf8.RuneSelf && r >= 0 && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

// checkBoundary looks at the rune on each side of [start, end). The start and
// the end of the text always count as a boundary.
func checkBoundary(text string, start, end int, boundary string) bool {
	var blocks func(rune) bool
	switch boundary {
	case "", BoundaryNone:
		return true
	case BoundaryDigit:
		blocks = unicode.IsDigit
	case BoundaryAlnum:
		blocks = func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	case BoundaryWord:
		blocks = func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	default:
		return false
	}
	if before := runeBefore(text, start); before != noRune && blocks(before) {
		return false
	}
	if after := runeAfter(text, end); after != noRune && blocks(after) {
		return false
	}
	return true
}

// contextHas reports whether one of words occurs, case-insensitively, within
// window runes before start or after end.
func contextHas(text string, start, end, window int, words []string) bool {
	from := start
	for i := 0; i < window && from > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	to := end
	for i := 0; i < window && to < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	before := strings.ToLower(text[from:start])
	after := strings.ToLower(text[end:to])
	for _, word := range words {
		if strings.Contains(before, word) || strings.Contains(after, word) {
			return true
		}
	}
	return false
}
