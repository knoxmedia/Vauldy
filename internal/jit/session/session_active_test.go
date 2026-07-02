package session

import "testing"

func TestHasActiveMedia(t *testing.T) {
	m := &Manager{sessions: map[string]*Session{
		"a": {MediaID: 10},
		"b": {MediaID: 20},
	}}
	if !m.HasActiveMedia(10) {
		t.Fatal("expected media 10 active")
	}
	if m.HasActiveMedia(99) {
		t.Fatal("expected media 99 inactive")
	}
	if (&Manager{}).HasActiveMedia(1) {
		t.Fatal("nil sessions should not be active")
	}
}
