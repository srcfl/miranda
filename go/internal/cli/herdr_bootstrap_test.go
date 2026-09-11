package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrAssetKey(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
		ok                 bool
	}{
		{"linux", "amd64", "linux-x86_64", true},
		{"linux", "arm64", "linux-aarch64", true},
		{"darwin", "amd64", "macos-x86_64", true},
		{"darwin", "arm64", "macos-aarch64", true},
		{"windows", "amd64", "", false},
		{"linux", "386", "", false},
	}
	for _, tc := range cases {
		got, err := herdrAssetKey(tc.goos, tc.goarch)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("herdrAssetKey(%s,%s) = %q, %v; want %q", tc.goos, tc.goarch, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("herdrAssetKey(%s,%s) = %q, want error", tc.goos, tc.goarch, got)
		}
	}
}

func TestIsHerdrLaunch(t *testing.T) {
	if !isHerdrLaunch([]string{"herdr"}) {
		t.Fatal("herdr")
	}
	if !isHerdrLaunch([]string{"/home/you/.local/bin/herdr"}) {
		t.Fatal("absolute herdr")
	}
	if isHerdrLaunch([]string{"tmux", "new"}) {
		t.Fatal("tmux")
	}
	if isHerdrLaunch(nil) {
		t.Fatal("nil")
	}
}

func TestHerdrEnsureSkipsNonHerdrLaunch(t *testing.T) {
	b := &herdrBootstrap{lookPath: lookPathOnly()}
	got, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"sh"})
	if err != nil || len(got) != 1 || got[0] != "sh" {
		t.Fatalf("ensure(sh) = %v, %v", got, err)
	}
}

func TestHerdrEnsureUsesLookPath(t *testing.T) {
	b := &herdrBootstrap{
		lookPath: lookPathOnly("herdr"),
		home:     func() (string, error) { return t.TempDir(), nil },
		get: func(string, int64) ([]byte, error) {
			t.Fatal("must not download when herdr is on PATH")
			return nil, nil
		},
	}
	got, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"herdr"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "/usr/bin/herdr" {
		t.Fatalf("launch = %v, want absolute PATH entry", got)
	}
}

func TestHerdrEnsureUsesLocalBinWhenNotOnPATH(t *testing.T) {
	home := t.TempDir()
	dest := localHerdrPath(home)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &herdrBootstrap{
		lookPath: lookPathOnly(),
		home:     func() (string, error) { return home, nil },
		get: func(string, int64) ([]byte, error) {
			t.Fatal("must not download when ~/.local/bin/herdr exists")
			return nil, nil
		},
	}
	got, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"herdr"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != dest {
		t.Fatalf("launch = %v, want %s", got, dest)
	}
}

func TestHerdrEnsureNonTTYRefuses(t *testing.T) {
	b := &herdrBootstrap{
		lookPath: lookPathOnly(),
		home:     func() (string, error) { return t.TempDir(), nil },
		isTTY:    false,
	}
	_, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"herdr"})
	if err == nil || !strings.Contains(err.Error(), herdrInstallHint) {
		t.Fatalf("expected the install-hint refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "herdr is not installed") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "tmux") || strings.Contains(err.Error(), "--shell sh") {
		t.Fatalf("herdr refusal must not offer a tmux/sh fallback: %v", err)
	}
}

func TestHerdrEnsureTTYDeclineRefusesNoFallback(t *testing.T) {
	out := &bytes.Buffer{}
	b := &herdrBootstrap{
		lookPath: lookPathOnly(),
		home:     func() (string, error) { return t.TempDir(), nil },
		get: func(string, int64) ([]byte, error) {
			t.Fatal("declined install must not download")
			return nil, nil
		},
		isTTY: true,
		in:    strings.NewReader("n\n"),
	}
	_, err := b.ensure(out, out, []string{"herdr"})
	if err == nil {
		t.Fatal("decline must refuse, not fall back")
	}
	if !strings.Contains(err.Error(), herdrInstallHint) {
		t.Fatalf("got %v", err)
	}
}

func TestHerdrEnsureTTYInstallsChecksumVerifiedBinary(t *testing.T) {
	payload := []byte("herdr-binary-bytes")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])
	manifest := fmt.Sprintf(`{"version":"0.9.0","assets":{"linux-x86_64":"https://example.test/herdr"},"sha256":{"linux-x86_64":%q}}`, hexSum)
	home := t.TempDir()
	var fetched []string
	out := &bytes.Buffer{}
	b := &herdrBootstrap{
		lookPath: lookPathOnly(),
		home:     func() (string, error) { return home, nil },
		get: func(url string, max int64) ([]byte, error) {
			fetched = append(fetched, url)
			switch url {
			case herdrManifestURL:
				return []byte(manifest), nil
			case "https://example.test/herdr":
				return payload, nil
			default:
				return nil, fmt.Errorf("unexpected GET %s", url)
			}
		},
		goos:   "linux",
		goarch: "amd64",
		isTTY:  true,
		in:     strings.NewReader("y\n"),
	}
	got, err := b.ensure(out, io.Discard, []string{"herdr"})
	if err != nil {
		t.Fatal(err)
	}
	want := localHerdrPath(home)
	if got[0] != want {
		t.Fatalf("launch = %v, want %s", got, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("installed bytes = %q", body)
	}
	st, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o111 == 0 {
		t.Fatalf("installed herdr is not executable: %o", perm)
	}
	if len(fetched) != 2 || fetched[0] != herdrManifestURL {
		t.Fatalf("fetches = %v", fetched)
	}
	if !strings.Contains(out.String(), "herdr 0.9.0") {
		t.Fatalf("expected version in output:\n%s", out.String())
	}
}

func TestHerdrEnsureChecksumMismatchRefuses(t *testing.T) {
	manifest := `{"version":"0.9.0","assets":{"linux-x86_64":"https://example.test/herdr"},"sha256":{"linux-x86_64":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
	b := &herdrBootstrap{
		lookPath: lookPathOnly(),
		home:     func() (string, error) { return t.TempDir(), nil },
		get: func(url string, max int64) ([]byte, error) {
			if url == herdrManifestURL {
				return []byte(manifest), nil
			}
			return []byte("tampered"), nil
		},
		goos:   "linux",
		goarch: "amd64",
		isTTY:  true,
		in:     strings.NewReader("\n"),
	}
	_, err := b.ensure(&bytes.Buffer{}, &bytes.Buffer{}, []string{"herdr"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum refusal, got %v", err)
	}
}

func TestNewHerdrBootstrapWiresRealDependencies(t *testing.T) {
	b := newHerdrBootstrap(false, strings.NewReader(""))
	if b.lookPath == nil || b.home == nil || b.get == nil || b.goos == "" || b.goarch == "" {
		t.Fatalf("newHerdrBootstrap left a field unset: %+v", b)
	}
}
