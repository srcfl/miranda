package agent

import "testing"

func TestActiveSessionsCounter(t *testing.T) {
	rt := &Runtime{}
	if rt.ActiveSessions() != 0 {
		t.Fatalf("fresh runtime active=%d", rt.ActiveSessions())
	}
	rt.sessionStarted()
	rt.sessionStarted()
	if rt.ActiveSessions() != 2 {
		t.Fatalf("after 2 starts active=%d", rt.ActiveSessions())
	}
	rt.sessionEnded()
	if rt.ActiveSessions() != 1 {
		t.Fatalf("after 1 end active=%d", rt.ActiveSessions())
	}
}
