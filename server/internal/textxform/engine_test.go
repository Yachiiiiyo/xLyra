package textxform

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"xlyra/server/internal/textmatch"
)

func find(text, value, typ string) textmatch.Span {
	start := strings.Index(text, value)
	if start < 0 {
		panic(value + " not in " + text)
	}
	return textmatch.Span{Start: start, End: start + len(value), Type: typ, Source: textmatch.SourceRule}
}

var testTypes = []string{"SECRET", "PHONE", "CUSTOM"}

func TestApplyPicksLongestThenDeclaredType(t *testing.T) {
	text := "call 13812345678 now"
	spans := []textmatch.Span{
		find(text, "13812345678", "PHONE"),
		find(text, "13812345678", "SECRET"),
		find(text, "1381234", "CUSTOM"),
		find(text, "13812345678", "PHONE"),
	}
	session := NewSession()
	result := Apply(session, []string{text}, [][]textmatch.Span{spans}, Options{Types: testTypes})
	if result.Texts[0] != "call [[SECRET_1]] now" || !result.Changed[0] {
		t.Fatalf("text = %q", result.Texts[0])
	}
	if original, ok := session.Lookup("[[SECRET_1]]"); !ok || original != "13812345678" {
		t.Fatalf("lookup = %q %v", original, ok)
	}
	if !reflect.DeepEqual(session.Counts(), map[string]int{"SECRET": 1}) || !session.HasReplacements() {
		t.Fatalf("counts = %#v", session.Counts())
	}
}

func TestApplySkipsNumbersAlreadyInTheRequest(t *testing.T) {
	texts := []string{"earlier note mentions [[PHONE_1]] and [[PHONE_3]]", "phones 13812345678 and 13900001111"}
	findings := [][]textmatch.Span{nil, {find(texts[1], "13812345678", "PHONE"), find(texts[1], "13900001111", "PHONE")}}
	session := NewSession()
	result := Apply(session, texts, findings, Options{Types: testTypes})
	if result.Texts[1] != "phones [[PHONE_2]] and [[PHONE_4]]" {
		t.Fatalf("text = %q", result.Texts[1])
	}
	if result.Changed[0] || result.Texts[0] != texts[0] {
		t.Fatal("a text without findings is unchanged")
	}
	if _, ok := session.Lookup("[[PHONE_1]]"); ok {
		t.Fatal("a placeholder the client wrote must not be restorable")
	}
	// A finding inside a literal placeholder is ignored.
	literal := "see [[PHONE_1]]"
	other := NewSession()
	out := Apply(other, []string{literal}, [][]textmatch.Span{{find(literal, "PHONE_1", "CUSTOM")}}, Options{Types: testTypes})
	if out.Changed[0] {
		t.Fatalf("finding inside a placeholder was replaced: %q", out.Texts[0])
	}
}

func TestApplyNumbersStayStableAcrossTurns(t *testing.T) {
	turn1 := []string{"我叫张三，电话 13812345678", "好的"}
	turn2 := append(append([]string(nil), turn1...), "再记一个 13900001111，还有张三的 13812345678")
	findingsOf := func(texts []string) [][]textmatch.Span {
		out := make([][]textmatch.Span, len(texts))
		for i, text := range texts {
			for _, value := range []string{"13812345678", "13900001111"} {
				if idx := strings.Index(text, value); idx >= 0 {
					out[i] = append(out[i], textmatch.Span{Start: idx, End: idx + len(value), Type: "PHONE"})
				}
			}
			if idx := strings.Index(text, "张三"); idx >= 0 && i == 0 {
				out[i] = append(out[i], textmatch.Span{Start: idx, End: idx + len("张三"), Type: "CUSTOM"})
			}
		}
		return out
	}
	opts := Options{Types: testTypes, PropagateSameValue: true, PropagateMinLength: 2}
	first := Apply(NewSession(), turn1, findingsOf(turn1), opts)
	second := Apply(NewSession(), turn2, findingsOf(turn2), opts)
	if first.Texts[0] != second.Texts[0] || first.Texts[0] != "我叫[[CUSTOM_1]]，电话 [[PHONE_1]]" {
		t.Fatalf("prefix changed between turns: %q vs %q", first.Texts[0], second.Texts[0])
	}
	if second.Texts[2] != "再记一个 [[PHONE_2]]，还有[[CUSTOM_1]]的 [[PHONE_1]]" {
		t.Fatalf("new turn = %q", second.Texts[2])
	}
}

func TestApplyPropagationRespectsMinimumAndOption(t *testing.T) {
	texts := []string{"code 甲 and 项目代号", "later 项目代号 again, 甲 alone"}
	findings := [][]textmatch.Span{{find(texts[0], "甲", "CUSTOM"), find(texts[0], "项目代号", "CUSTOM")}, nil}
	on := Apply(NewSession(), texts, findings, Options{Types: testTypes, PropagateSameValue: true, PropagateMinLength: 2})
	if on.Texts[1] != "later [[CUSTOM_2]] again, 甲 alone" {
		t.Fatalf("propagated = %q", on.Texts[1])
	}
	if len(on.Applied[1]) != 1 || !on.Applied[1][0].Propagated {
		t.Fatalf("applied = %#v", on.Applied[1])
	}
	off := Apply(NewSession(), texts, findings, Options{Types: testTypes})
	if off.Changed[1] {
		t.Fatalf("propagation off changed %q", off.Texts[1])
	}
}

func TestSessionContext(t *testing.T) {
	if SessionFromContext(context.Background()) != nil || RestoringSessionFromContext(context.Background()) != nil {
		t.Fatal("no session by default")
	}
	session := NewSession()
	ctx := WithSession(context.Background(), session)
	if SessionFromContext(ctx) != session {
		t.Fatal("session round trip")
	}
	if RestoringSessionFromContext(ctx) != nil {
		t.Fatal("an empty or irreversible session restores nothing")
	}
	session.Reversible = true
	session.Allocate("PHONE", "1")
	if RestoringSessionFromContext(ctx) != session {
		t.Fatal("a reversible session with placeholders restores")
	}
	if Placeholder("PHONE", 12) != "[[PHONE_12]]" || len(Placeholder(strings.Repeat("A", 24), 999999999)) > MaxPlaceholderLen {
		t.Fatal("placeholder format")
	}
}
