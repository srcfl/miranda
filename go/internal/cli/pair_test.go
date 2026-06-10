package cli

import "testing"

func TestClassifyPair(t *testing.T) {
	if m, code, err := classifyPair(nil); err != nil || m != pairResponder || code != "" {
		t.Fatalf("no args = %v,%q,%v; want responder", m, code, err)
	}
	if m, code, err := classifyPair([]string{"ABC123"}); err != nil || m != pairInitiator || code != "ABC123" {
		t.Fatalf("one arg = %v,%q,%v; want initiator ABC123", m, code, err)
	}
	if _, _, err := classifyPair([]string{"a", "b"}); err == nil {
		t.Fatal("two args should error")
	}
}
