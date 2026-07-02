package subtitle

import "testing"

func TestReconcileTranslatedLines(t *testing.T) {
	got := reconcileTranslatedLines(3, []string{"a", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "" {
		t.Fatalf("reconcile short: %#v", got)
	}
	got = reconcileTranslatedLines(2, []string{"a", "b", "c"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("reconcile long: %#v", got)
	}
}

func TestParseTranslateLinesResponse(t *testing.T) {
	lines, err := parseTranslateLinesResponse(`{"count":2,"lines":["Hello","World"]}`, 2)
	if err != nil || len(lines) != 2 || lines[0] != "Hello" {
		t.Fatalf("parse object: %v %#v", err, lines)
	}
	lines, err = parseTranslateLinesResponse(`["A","B"]`, 2)
	if err != nil || lines[0] != "A" {
		t.Fatalf("parse array: %v %#v", err, lines)
	}
	lines, err = parseTranslateLinesResponse(`{"0":"X","1":"Y"}`, 2)
	if err != nil || lines[0] != "X" || lines[1] != "Y" {
		t.Fatalf("parse indexed: %v %#v", err, lines)
	}
}
