package handler

import (
	"strings"
	"testing"
)

func TestSplitMediaSearchTokens(t *testing.T) {
	got := splitMediaSearchTokens("  外出  2005  ")
	if len(got) != 2 || got[0] != "外出" || got[1] != "2005" {
		t.Fatalf("split tokens: %#v", got)
	}
	if splitMediaSearchTokens("   ") != nil {
		t.Fatalf("empty query should return nil tokens")
	}
}

func TestAppendMediaTextSearchFilterArgs(t *testing.T) {
	q, args := appendMediaTextSearchFilter("SELECT 1 WHERE 1=1", nil, "外出 2005")
	if !strings.Contains(q, "m.title LIKE") {
		t.Fatalf("missing title clause: %q", q)
	}
	if len(args) != mediaSearchLikePerToken*2 {
		t.Fatalf("args=%d want %d", len(args), mediaSearchLikePerToken*2)
	}
}

func TestMediaSearchOrClausePlaceholderCount(t *testing.T) {
	clause := mediaSearchOrClause()
	if strings.Count(clause, "?") != mediaSearchLikePerToken {
		t.Fatalf("placeholder count=%d constant=%d", strings.Count(clause, "?"), mediaSearchLikePerToken)
	}
}
