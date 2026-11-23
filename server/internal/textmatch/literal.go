package textmatch

import (
	"unicode"
	"unicode/utf8"
)

// LiteralSet finds many literal words in one pass over a text (Aho-Corasick
// over runes). Folding lower-cases each rune on its own, so a word and the
// text always keep one rune per rune and every match maps back to exact byte
// offsets of the original text.
type LiteralSet struct {
	nodes         []acNode
	words         []acWord
	caseSensitive bool
}

type acNode struct {
	next   map[rune]int32
	fail   int32
	output []int32
	// dict is the nearest node on the fail chain with an output.
	dict int32
}

type acWord struct {
	runes int
}

// LiteralMatch is one occurrence of word Index at [Start, End) bytes.
type LiteralMatch struct {
	Start int
	End   int
	Index int
}

func foldRune(r rune, caseSensitive bool) rune {
	if caseSensitive {
		return r
	}
	return unicode.ToLower(r)
}

// NewLiteralSet compiles words. Empty words are ignored; Index in a match is
// the position of the word in words.
func NewLiteralSet(words []string, caseSensitive bool) *LiteralSet {
	set := &LiteralSet{nodes: []acNode{{fail: 0, dict: -1}}, words: make([]acWord, len(words)), caseSensitive: caseSensitive}
	for index, word := range words {
		if word == "" {
			continue
		}
		state := int32(0)
		count := 0
		for _, r := range word {
			r = foldRune(r, caseSensitive)
			count++
			node := &set.nodes[state]
			child, ok := node.next[r]
			if !ok {
				child = int32(len(set.nodes))
				if node.next == nil {
					node.next = map[rune]int32{}
				}
				node.next[r] = child
				set.nodes = append(set.nodes, acNode{dict: -1})
			}
			state = child
		}
		set.words[index] = acWord{runes: count}
		set.nodes[state].output = append(set.nodes[state].output, int32(index))
	}
	queue := make([]int32, 0, len(set.nodes))
	for _, child := range set.nodes[0].next {
		set.nodes[child].fail = 0
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for r, child := range set.nodes[state].next {
			queue = append(queue, child)
			fail := set.nodes[state].fail
			for {
				if next, ok := set.nodes[fail].next[r]; ok && next != child {
					set.nodes[child].fail = next
					break
				}
				if fail == 0 {
					set.nodes[child].fail = 0
					break
				}
				fail = set.nodes[fail].fail
			}
			failNode := set.nodes[child].fail
			if len(set.nodes[failNode].output) > 0 {
				set.nodes[child].dict = failNode
			} else {
				set.nodes[child].dict = set.nodes[failNode].dict
			}
		}
	}
	return set
}

// Empty reports whether the set has no word.
func (s *LiteralSet) Empty() bool {
	return s == nil || len(s.nodes) <= 1
}

// FindAll returns every occurrence of every word, overlapping ones included,
// ordered by end offset.
func (s *LiteralSet) FindAll(text string) []LiteralMatch {
	if s.Empty() || text == "" {
		return nil
	}
	// starts holds the byte offset of the runes seen so far, enough to map
	// a match of n runes back to its first byte.
	starts := make([]int, 0, utf8.RuneCountInString(text))
	var out []LiteralMatch
	state := int32(0)
	for offset, r := range text {
		starts = append(starts, offset)
		end := offset + utf8.RuneLen(r)
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(text[offset:]); size == 1 {
				end = offset + 1
			}
		}
		folded := foldRune(r, s.caseSensitive)
		for {
			if next, ok := s.nodes[state].next[folded]; ok {
				state = next
				break
			}
			if state == 0 {
				break
			}
			state = s.nodes[state].fail
		}
		emit := func(node int32) {
			for _, index := range s.nodes[node].output {
				runes := s.words[index].runes
				first := len(starts) - runes
				if first < 0 {
					continue
				}
				out = append(out, LiteralMatch{Start: starts[first], End: end, Index: int(index)})
			}
		}
		emit(state)
		for node := s.nodes[state].dict; node > 0; node = s.nodes[node].dict {
			emit(node)
		}
	}
	return out
}
