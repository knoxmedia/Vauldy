package taskalign

import "testing"

func TestMapDomainStatus(t *testing.T) {
	cases := []struct {
		typ, in, want string
	}{
		{"subtitle", "pending", "waiting"},
		{"subtitle", "running", "running"},
		{"subtitle", "done", "done"},
		{"subtitle", "failed", "failed"},
		{"preview", "ready", "done"},
		{"preview", "waiting", "waiting"},
		{"preview", "failed", "failed"},
		{"atrack", "waiting", "waiting"},
		{"atrack", "done", "done"},
		{"keyframe", "failed", "failed"},
		{"encrypt", "waiting", "waiting"},
	}
	for _, tc := range cases {
		if got := MapDomainOrQueue(tc.typ, tc.in); got != tc.want {
			t.Fatalf("%s %q → %q want %q", tc.typ, tc.in, got, tc.want)
		}
	}
}

func TestSynthesizePriority(t *testing.T) {
	if got := Synthesize("waiting", "running", "subtitle"); got != "running" {
		t.Fatalf("got %q", got)
	}
	if got := Synthesize("failed", "pending", "subtitle"); got != "failed" {
		t.Fatalf("failed+pending → %q want failed", got)
	}
	if got := Synthesize("cancelled", "waiting", "subtitle"); got != "cancelled" {
		t.Fatalf("cancelled+waiting → %q", got)
	}
	if got := Synthesize("", "pending", "subtitle"); got != "waiting" {
		t.Fatalf("domain-only pending → %q", got)
	}
	if got := Synthesize("done", "", "subtitle"); got != "done" {
		t.Fatalf("queue-only done → %q", got)
	}
}
