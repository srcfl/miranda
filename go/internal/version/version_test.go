package version

import (
	"strings"
	"testing"
)

func TestStringIncludesVersionCommitDate(t *testing.T) {
	Version, Commit, Date = "1.2.3", "abc1234", "2026-06-08T00:00:00Z"
	got := String()
	for _, want := range []string{"1.2.3", "abc1234", "2026-06-08"} {
		if !strings.Contains(got, want) {
			t.Fatalf("String()=%q missing %q", got, want)
		}
	}
}

func TestStringDefaultsToDev(t *testing.T) {
	Version, Commit, Date = "dev", "none", "unknown"
	if got := String(); !strings.Contains(got, "dev") {
		t.Fatalf("String()=%q, want it to contain \"dev\"", got)
	}
}
