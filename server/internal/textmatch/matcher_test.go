package textmatch

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func cnID(prefix17 string) string {
	sum := 0
	for i := range 17 {
		sum += int(prefix17[i]-'0') * cnIDWeights[i]
	}
	return prefix17 + string(cnIDChecks[sum%11])
}

func TestValidators(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	valid := cnID("11010519491231002")
	cases := []struct {
		name, validator, value string
		want                   bool
	}{
		{"luhn visa", ValidateLuhn, "4111111111111111", true},
		{"luhn spaced", ValidateLuhn, "4111 1111 1111 1111", true},
		{"luhn bad digit", ValidateLuhn, "4111111111111112", false},
		{"luhn too short", ValidateLuhn, "4111111111", false},
		{"ipv4", ValidateIPv4, "192.168.1.20", true},
		{"ipv4 range", ValidateIPv4, "256.1.1.1", false},
		{"ipv4 is not ipv6", ValidateIPv6, "10.0.0.1", false},
		{"ipv6", ValidateIPv6, "2001:db8::1", true},
		{"ipv6 bad", ValidateIPv6, "2001:db8:::1", false},
		{"email", ValidateEmail, "alice@example.com", true},
		{"email no dot", ValidateEmail, "alice@localhost", false},
		{"email display name", ValidateEmail, "Alice <alice@example.com>", false},
		{"email trailing dot", ValidateEmail, "alice@example.", false},
		{"none", "", "anything", true},
		{"unknown", "iban", "x", false},
	}
	for _, tc := range cases {
		if got := Validate(tc.validator, tc.value); got != tc.want {
			t.Fatalf("%s: Validate(%q, %q) = %v", tc.name, tc.validator, tc.value, got)
		}
	}
	if !ValidCNIDCard(valid, now) || !ValidCNIDCard(strings.ToLower(cnID("11010519900307123")), now) && strings.HasSuffix(cnID("11010519900307123"), "X") {
		t.Fatalf("valid id card rejected: %s", valid)
	}
	broken := valid[:17] + "0"
	if valid[17] == '0' {
		broken = valid[:17] + "1"
	}
	for _, bad := range []string{broken, cnID("11010518991231002"), cnID("11010520991231002"), cnID("11010519490230002"), valid[:17]} {
		if ValidCNIDCard(bad, now) {
			t.Fatalf("invalid id card accepted: %s", bad)
		}
	}
	if Entropy("aaaa") != 0 || Entropy("abcd") != 2 || Entropy("") != 0 {
		t.Fatalf("entropy: %v %v", Entropy("aaaa"), Entropy("abcd"))
	}
}

func spanTexts(text string, spans []Span) []string {
	SortSpans(spans)
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, span.Type+":"+text[span.Start:span.End])
	}
	return out
}

func TestRulesBoundariesGroupsAndContext(t *testing.T) {
	m, err := Compile(Spec{
		Types: []string{"SECRET", "PHONE", "CARD"},
		Rules: []RuleSpec{
			{Type: "PHONE", Pattern: `(?:\+?86[- ]?)?(1[3-9]\d{9})`, Group: 1, Boundary: BoundaryDigit},
			{Type: "SECRET", Pattern: `[A-Za-z0-9]{16,}`, Boundary: BoundaryWord, MinEntropy: 3.5, ContextAny: []string{"token", "密钥"}, ContextWindow: 8},
			{Type: "CARD", Pattern: `\d[\d ]{11,22}\d`, Validate: ValidateLuhn, Boundary: BoundaryDigit},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "电话+86 13900001111，旧号 213900001111；TOKEN: Zx9Kq2Lm8Pw4Rt6Y 卡 4111 1111 1111 1111 和 aaaaaaaaaaaaaaaaaaaa token"
	got := spanTexts(text, m.Match(text))
	want := []string{"PHONE:13900001111", "SECRET:Zx9Kq2Lm8Pw4Rt6Y", "CARD:4111 1111 1111 1111"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	noContext := "随便 Zx9Kq2Lm8Pw4Rt6Y 写点什么"
	if spans := m.Match(noContext); len(spans) != 0 {
		t.Fatalf("secret without context: %q", spanTexts(noContext, spans))
	}
	cjkContext := "我的密钥：Zx9Kq2Lm8Pw4Rt6Y"
	if got := spanTexts(cjkContext, m.Match(cjkContext)); len(got) != 1 {
		t.Fatalf("context in CJK: %q", got)
	}
}

func TestBoundaryKinds(t *testing.T) {
	cases := []struct {
		boundary string
		text     string
		want     int
	}{
		{BoundaryDigit, "x123", 1},
		{BoundaryDigit, "9123", 0},
		{BoundaryAlnum, "a123", 0},
		{BoundaryAlnum, "中123", 0},
		{BoundaryAlnum, "-123-", 1},
		{BoundaryWord, "_123", 0},
		{BoundaryWord, "(123)", 1},
		{BoundaryNone, "91239", 1},
	}
	for _, tc := range cases {
		m, err := Compile(Spec{Types: []string{"N"}, Rules: []RuleSpec{{Type: "N", Pattern: `123`, Boundary: tc.boundary}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(m.Match(tc.text)); got != tc.want {
			t.Fatalf("%s in %q: %d", tc.boundary, tc.text, got)
		}
	}
}

func TestDictionaryOffsetsWithMultibyteAndFolding(t *testing.T) {
	m, err := Compile(Spec{
		Types: []string{"CUSTOM", "PERSON", "ORG"},
		Dictionaries: []DictionarySpec{
			{Type: "CUSTOM", Text: "Project İris\n星辰计划\n\n  alpha  \nACME\tORG\n张三\tPERSON\n"},
		},
		Allowlist: "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	// İ lower-cases to a one-byte i: offsets must still point at the original bytes.
	text := "😀PROJECT İRIS 与 星辰计划；alphabet 不算，Alpha 算；ACME 在白名单；张三来了"
	got := spanTexts(text, m.Match(text))
	want := []string{"CUSTOM:PROJECT İRIS", "CUSTOM:星辰计划", "CUSTOM:Alpha", "PERSON:张三"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	sensitive, err := Compile(Spec{Types: []string{"CUSTOM"}, Dictionaries: []DictionarySpec{{Type: "CUSTOM", CaseSensitive: true, Text: "Alpha"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := spanTexts("alpha Alpha", sensitive.Match("alpha Alpha")); !reflect.DeepEqual(got, []string{"CUSTOM:Alpha"}) {
		t.Fatalf("case sensitive: %q", got)
	}
	if _, err := Compile(Spec{Types: []string{"CUSTOM"}, Dictionaries: []DictionarySpec{{Type: "CUSTOM", Text: "x\tNOPE"}}}); err == nil {
		t.Fatal("an undeclared override type must fail")
	}
}

func TestLiteralSetOverlapsAndInvalidUTF8(t *testing.T) {
	set := NewLiteralSet([]string{"he", "she", "his", "hers"}, true)
	text := "ushers"
	var got []string
	for _, match := range set.FindAll(text) {
		got = append(got, text[match.Start:match.End])
	}
	if !reflect.DeepEqual(got, []string{"she", "he", "hers"}) {
		t.Fatalf("overlapping matches: %q", got)
	}
	bad := "a\xffhe"
	matches := set.FindAll(bad)
	if len(matches) != 1 || bad[matches[0].Start:matches[0].End] != "he" {
		t.Fatalf("invalid utf-8: %#v", matches)
	}
}

func TestEnabledByAndAllowlist(t *testing.T) {
	m, err := Compile(Spec{
		Types:     []string{"PHONE", "EMAIL"},
		Disabled:  map[string]bool{"PHONE": true},
		Rules:     []RuleSpec{{Type: "PHONE", Pattern: `1\d{10}`}, {Type: "EMAIL", Pattern: `\S+@\S+`, Validate: ValidateEmail}},
		Allowlist: "Support@Example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := "13812345678 bob@example.com support@example.com"
	if got := spanTexts(text, m.Match(text)); !reflect.DeepEqual(got, []string{"EMAIL:bob@example.com"}) {
		t.Fatalf("got %q", got)
	}
	if m.Keep("PHONE", "13812345678") || !m.Keep("EMAIL", "x@y.z") || m.Keep("NAME", "x") || m.Keep("EMAIL", "SUPPORT@example.com") {
		t.Fatal("Keep must apply switches, declarations and the allowlist")
	}
	if m.TypeRank("EMAIL") != 1 || m.TypeRank("NOPE") != 2 {
		t.Fatal("type rank follows declaration order")
	}
}

func TestSegmentCacheKeysAndEviction(t *testing.T) {
	keyA := CacheKey("sk-secret", "user", "message.text", "hash")
	if keyA == CacheKey("sk-secret", "tool", "message.text", "hash") ||
		keyA == CacheKey("sk-secret", "user", "tool.result", "hash") ||
		keyA == CacheKey("sk-secret", "user", "message.text", "other") ||
		keyA == CacheKey("sk-secrex", "user", "message.text", "hash") {
		t.Fatal("cache keys must differ by text, role, field and config hash")
	}
	if strings.Contains(keyA, "sk-secret") {
		t.Fatal("a cache key must not contain the text")
	}
	cache := NewSegmentCache(cacheShards * 1024)
	spans := []CachedSpan{{Start: 0, End: 9, Type: "SECRET", Source: SourceRule}}
	cache.Put(keyA, spans)
	got, ok := cache.Get(keyA)
	if !ok || !reflect.DeepEqual(got, spans) {
		t.Fatalf("get = %#v %v", got, ok)
	}
	got[0].Type = "CHANGED"
	if again, _ := cache.Get(keyA); again[0].Type != "SECRET" {
		t.Fatal("Get must return a copy")
	}
	if _, ok := cache.Get(CacheKey("sk-secret", "tool", "message.text", "hash")); ok {
		t.Fatal("another role must miss")
	}
	for i := range 500 {
		cache.Put(CacheKey(strings.Repeat("x", i), "user", "message.text", "hash"), spans)
	}
	if cache.Bytes() > cacheShards*1024 {
		t.Fatalf("cache uses %d bytes", cache.Bytes())
	}
	if cache.Len() >= 500 {
		t.Fatalf("cache kept %d entries", cache.Len())
	}
	if typ := reflect.TypeFor[CachedSpan](); typ.NumField() != 5 {
		t.Fatal("CachedSpan holds offsets and labels only")
	}
}
