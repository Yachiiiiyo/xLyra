// Package textxform turns findings into placeholders for one request and
// keeps the two-way mapping in memory only, so responses can be restored. A
// session never outlives its request and is never stored or logged.
package textxform

import (
	"context"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// MaxPlaceholderLen is the longest placeholder: [[ + 24-character type + _ +
// a number + ]]. Stream filters hold back at most this many bytes.
const MaxPlaceholderLen = 48

// PlaceholderPattern matches [[TYPE_N]] with N a positive number.
var PlaceholderPattern = regexp.MustCompile(`\[\[([A-Z][A-Z0-9_]{0,23})_([1-9][0-9]*)\]\]`)

// Placeholder formats the placeholder of type typ and number n.
func Placeholder(typ string, n int) string {
	return "[[" + typ + "_" + strconv.Itoa(n) + "]]"
}

// Session is the placeholder mapping of one request.
type Session struct {
	mu       sync.RWMutex
	forward  map[string]string
	byValue  map[[2]string]string
	anyType  map[string]string
	next     map[string]int
	occupied map[string]map[int]bool
	counts   map[string]int
	created  int
	// RestoreReasoning says whether response reasoning is restored too.
	RestoreReasoning bool
	// Reversible says whether responses are restored at all.
	Reversible bool
}

// NewSession starts an empty mapping.
func NewSession() *Session {
	return &Session{
		forward:  map[string]string{},
		byValue:  map[[2]string]string{},
		anyType:  map[string]string{},
		next:     map[string]int{},
		occupied: map[string]map[int]bool{},
		counts:   map[string]int{},
	}
}

// Occupy marks placeholders already in the request text, so new ones skip
// their numbers and a literal [[TYPE_N]] the client wrote is never restored.
func (s *Session) Occupy(text string) {
	if s == nil || !strings.Contains(text, "[[") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, match := range PlaceholderPattern.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		if s.occupied[match[1]] == nil {
			s.occupied[match[1]] = map[int]bool{}
		}
		s.occupied[match[1]][n] = true
	}
}

// Allocate returns the placeholder of (typ, original), making a new one with
// the smallest free number when the pair has none yet.
func (s *Session) Allocate(typ, original string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocateLocked(typ, original)
}

func (s *Session) allocateLocked(typ, original string) string {
	key := [2]string{typ, original}
	if token, ok := s.byValue[key]; ok {
		return token
	}
	n := max(s.next[typ], 1)
	for s.occupied[typ][n] {
		n++
	}
	s.next[typ] = n + 1
	token := Placeholder(typ, n)
	s.forward[token] = original
	s.byValue[key] = token
	if _, ok := s.anyType[original]; !ok {
		s.anyType[original] = token
	}
	s.created++
	return token
}

// PlaceholderFor returns the placeholder for original: the one of the same
// type if any, otherwise, when anyType is set, one of any type.
func (s *Session) PlaceholderFor(typ, original string, anyType bool) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if token, ok := s.byValue[[2]string{typ, original}]; ok {
		return token, true
	}
	if anyType {
		token, ok := s.anyType[original]
		return token, ok
	}
	return "", false
}

// Lookup returns the original text of a placeholder of this session.
func (s *Session) Lookup(token string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	original, ok := s.forward[token]
	return original, ok
}

// Originals lists every original with its placeholder, for turning text the
// gateway restored back into placeholders before it goes upstream again.
func (s *Session) Originals() (originals, tokens []string) {
	if s == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	originals = make([]string, 0, len(s.byValue))
	tokens = make([]string, 0, len(s.byValue))
	for key, token := range s.byValue {
		originals = append(originals, key[1])
		tokens = append(tokens, token)
	}
	return originals, tokens
}

// HasReplacements reports whether the request got at least one placeholder.
func (s *Session) HasReplacements() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.created > 0
}

// Len is the number of placeholders.
func (s *Session) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.forward)
}

func (s *Session) count(typ string) {
	s.mu.Lock()
	s.counts[typ]++
	s.mu.Unlock()
}

// Counts is how many replacements each type got. It holds no text.
func (s *Session) Counts() map[string]int {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.counts)
}

type sessionKey struct{}

// WithSession attaches a session to a request context.
func WithSession(ctx context.Context, session *Session) context.Context {
	if session == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionKey{}, session)
}

// SessionFromContext returns the session of a request, or nil.
func SessionFromContext(ctx context.Context) *Session {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(sessionKey{}).(*Session)
	return session
}

// RestoringSessionFromContext returns the session when responses must be restored.
func RestoringSessionFromContext(ctx context.Context) *Session {
	session := SessionFromContext(ctx)
	if session == nil || !session.Reversible || session.Len() == 0 {
		return nil
	}
	return session
}
