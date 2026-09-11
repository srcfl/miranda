package cli

import (
	"strings"
	"testing"
)

func TestResolveLaunch(t *testing.T) {
	notice := herdrPhoneNotice()
	cases := []struct {
		name        string
		engineFlag  string
		shell       string
		engineSet   bool
		shellSet    bool
		persisted   string
		wantLaunch  string
		wantPersist string
		wantNotice  bool
		wantErr     string
	}{
		{
			name:        "engine herdr",
			engineFlag:  "herdr",
			engineSet:   true,
			wantLaunch:  "herdr",
			wantPersist: "herdr",
			wantNotice:  true,
		},
		{
			name:        "engine tmux",
			engineFlag:  "tmux",
			engineSet:   true,
			wantLaunch:  "tmux:new:-A:-s:main",
			wantPersist: "tmux",
		},
		{
			name:       "engine unknown",
			engineFlag: "zellij",
			engineSet:  true,
			wantErr:    `unknown engine "zellij" (known: tmux, herdr)`,
		},
		{
			name:       "engine and shell together",
			engineFlag: "herdr",
			shell:      "sh",
			engineSet:  true,
			shellSet:   true,
			wantErr:    "--engine and --shell cannot be used together",
		},
		{
			name:       "shell only does not persist",
			shell:      "herdr",
			shellSet:   true,
			persisted:  "tmux",
			wantLaunch: "herdr",
		},
		{
			name:        "persisted herdr without flags",
			shell:       defaultTmuxShell,
			persisted:   "herdr",
			wantLaunch:  "herdr",
			wantPersist: "herdr",
			wantNotice:  true,
		},
		{
			name:       "default is tmux",
			shell:      defaultTmuxShell,
			wantLaunch: "tmux:new:-A:-s:main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			launch, persist, gotNotice, err := resolveLaunch(tc.engineFlag, tc.shell, tc.engineSet, tc.shellSet, tc.persisted)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := strings.Join(launch, ":"); got != tc.wantLaunch {
				t.Errorf("launch = %q, want %q", got, tc.wantLaunch)
			}
			if persist != tc.wantPersist {
				t.Errorf("persist = %q, want %q", persist, tc.wantPersist)
			}
			if tc.wantNotice {
				if gotNotice != notice {
					t.Errorf("notice mismatch:\n%s", gotNotice)
				}
			} else if gotNotice != "" {
				t.Errorf("notice = %q, want empty", gotNotice)
			}
		})
	}
}

func TestHerdrPhoneNoticeNamesThePhonePath(t *testing.T) {
	s := herdrPhoneNotice()
	for _, want := range []string{"phone", "same session", "Ctrl-B", "tmux-only"} {
		if !strings.Contains(s, want) {
			t.Errorf("notice missing %q:\n%s", want, s)
		}
	}
}
