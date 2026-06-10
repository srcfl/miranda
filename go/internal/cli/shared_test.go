package cli

import "testing"

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":            nil,
		"  ":          nil,
		"a":           {"a"},
		"a,b,c":       {"a", "b", "c"},
		" a , b ,, c": {"a", "b", "c"}, // trims, drops empties
	}
	for in, want := range cases {
		got := splitCSV(in)
		if len(got) != len(want) {
			t.Fatalf("splitCSV(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitCSV(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestParsePrefix(t *testing.T) {
	ok := map[string]byte{"ctrl-o": 0x0f, "c-a": 0x01, "^o": 0x0f, "ctrl-space": 0x00, "ctrl-]": 0x1d}
	for in, want := range ok {
		b, _, err := parsePrefix(in)
		if err != nil || b != want {
			t.Fatalf("parsePrefix(%q) = %#x, %v, want %#x", in, b, err, want)
		}
	}
	if _, _, err := parsePrefix("ctrl-99"); err == nil {
		t.Fatal("parsePrefix(ctrl-99) should error")
	}
}
