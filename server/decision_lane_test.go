package server

import (
	"testing"
)

func TestDedupeTokensJaccard(t *testing.T) {
	a := tokens("Heap buffer overflow in EXCEL! record parser (BIFF8)")
	b := tokens("heap buffer overflow when parsing BIFF record")
	if j := jaccard(a, b); j < 0.2 {
		t.Fatalf("related texts should overlap, got %.2f (%v vs %v)", j, a, b)
	}
	if j := jaccard(a, tokens("SQL injection through login form parameters")); j > 0.05 {
		t.Fatalf("unrelated texts must not overlap, got %.2f", j)
	}
	if j := jaccard(tokens("a b c"), tokens("")); j != 0 {
		t.Fatalf("empty side must be 0, got %.2f", j)
	}
	// short words are noise and are dropped
	if len(tokens("the a of in to is")) != 0 {
		t.Fatal("short words must be dropped")
	}
}

func TestDecisionInflightKeyScoped(t *testing.T) {
	// the same record id under two scopes must not collide (a finding id and a
	// retest conversation id are different id spaces)
	if decisionInflightKey(DecisionScopeFinding, 7) == decisionInflightKey(DecisionScopeRetest, 7) {
		t.Fatal("scope must be part of the inflight key")
	}
}

func TestClipStrKeepsHead(t *testing.T) {
	if got := clipStr("abcdef", 3); got != "abc…" {
		t.Fatalf("got %q", got)
	}
	if got := clipStr("abc", 10); got != "abc" {
		t.Fatalf("short strings unchanged, got %q", got)
	}
}
