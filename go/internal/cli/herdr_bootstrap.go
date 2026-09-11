package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	herdrManifestURL = "https://herdr.dev/latest.json"
	herdrInstallHint = "curl -fsSL https://herdr.dev/install.sh | sh"
	herdrWhy         = "herdr is the multiplexer on this machine — Miranda will attach your phone to the same session."
	herdrManifestMax = 2 << 20  // 2 MiB
	herdrBinaryMax   = 80 << 20 // 80 MiB
	herdrHTTPTimeout = 120 * time.Second
)

// herdrBootstrap decides whether `mir up --engine herdr` can launch herdr,
// offering to install it into ~/.local/bin when it is missing. Unlike tmux,
// a missing herdr never falls back to another engine — the user asked for
// herdr, and a silent tmux session would be a different product. OS/process
// dependencies are fields so the decision tree unit-tests without the
// network.
type herdrBootstrap struct {
	lookPath func(string) (string, error)
	home     func() (string, error)
	get      func(url string, max int64) ([]byte, error)
	goos     string
	goarch   string
	isTTY    bool
	in       io.Reader
}

func newHerdrBootstrap(isTTY bool, in io.Reader) *herdrBootstrap {
	return &herdrBootstrap{
		lookPath: exec.LookPath,
		home:     os.UserHomeDir,
		get:      httpGetBytes,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		isTTY:    isTTY,
		in:       in,
	}
}

func isHerdrLaunch(launch []string) bool {
	return len(launch) > 0 && filepath.Base(launch[0]) == "herdr"
}

func herdrAssetKey(goos, goarch string) (string, error) {
	var osName string
	switch goos {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "macos"
	default:
		return "", fmt.Errorf("herdr has no binary for %s (Miranda agents target macOS and Linux)", goos)
	}
	var arch string
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("herdr has no binary for %s/%s", goos, goarch)
	}
	return osName + "-" + arch, nil
}

// localHerdrPath is where the official installer and this bootstrap write the
// binary. Looked up even when that directory is not on PATH.
func localHerdrPath(home string) string {
	return filepath.Join(home, ".local", "bin", "herdr")
}

func herdrOnPathOrLocal(lookPath func(string) (string, error), home func() (string, error)) (string, bool) {
	if p, err := lookPath("herdr"); err == nil && p != "" {
		return p, true
	}
	if home == nil {
		return "", false
	}
	h, err := home()
	if err != nil || h == "" {
		return "", false
	}
	p := localHerdrPath(h)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, true
	}
	return "", false
}

func (b *herdrBootstrap) find(argv0 string) string {
	if filepath.IsAbs(argv0) {
		if fi, err := os.Stat(argv0); err == nil && !fi.IsDir() {
			return argv0
		}
	}
	if p, ok := herdrOnPathOrLocal(b.lookPath, b.home); ok {
		return p
	}
	return ""
}

func withHerdrBin(launch []string, path string) []string {
	out := make([]string, len(launch))
	copy(out, launch)
	out[0] = path
	return out
}

// ensure makes a herdr launch runnable. If herdr is already on PATH or in
// ~/.local/bin, launch is rewritten to that absolute path. Otherwise:
//   - no TTY: refused with the official install one-liner.
//   - TTY, user accepts: download latest.json, verify SHA-256, write
//     ~/.local/bin/herdr.
//   - TTY, declined or install failed: refused. Never fall back to tmux.
func (b *herdrBootstrap) ensure(out, errOut io.Writer, launch []string) ([]string, error) {
	if !isHerdrLaunch(launch) {
		return launch, nil
	}
	if path := b.find(launch[0]); path != "" {
		return withHerdrBin(launch, path), nil
	}
	if !b.isTTY {
		return nil, fmt.Errorf("herdr is not installed: run `%s`, then re-run `mir up --engine herdr`", herdrInstallHint)
	}

	fmt.Fprintln(out, herdrWhy)
	fmt.Fprintf(out, "Install herdr to ~/.local/bin with a checksum-verified download? [Y/n] ")
	line, _ := bufio.NewReader(b.in).ReadString('\n')
	if !installConsent(line) {
		return nil, fmt.Errorf("herdr is not installed: run `%s`, then re-run `mir up --engine herdr`", herdrInstallHint)
	}

	fmt.Fprintln(out, "Installing herdr…")
	path, err := b.install(out, errOut)
	if err != nil {
		return nil, fmt.Errorf("herdr install failed: %w; run `%s` and re-run `mir up --engine herdr`", err, herdrInstallHint)
	}
	fmt.Fprintf(out, "herdr installed at %s\n", path)
	return withHerdrBin(launch, path), nil
}

type herdrManifest struct {
	Version string            `json:"version"`
	Assets  map[string]string `json:"assets"`
	SHA256  map[string]string `json:"sha256"`
}

func (b *herdrBootstrap) install(out, errOut io.Writer) (string, error) {
	_ = errOut
	key, err := herdrAssetKey(b.goos, b.goarch)
	if err != nil {
		return "", err
	}
	raw, err := b.get(herdrManifestURL, herdrManifestMax)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", herdrManifestURL, err)
	}
	var man herdrManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return "", fmt.Errorf("parse latest.json: %w", err)
	}
	assetURL, ok := man.Assets[key]
	if !ok || strings.TrimSpace(assetURL) == "" {
		return "", fmt.Errorf("latest.json has no asset for %s", key)
	}
	want := strings.ToLower(strings.TrimSpace(man.SHA256[key]))
	if len(want) != 64 {
		return "", fmt.Errorf("latest.json has no SHA-256 for %s", key)
	}
	if man.Version != "" {
		fmt.Fprintf(out, "downloading herdr %s (%s)…\n", man.Version, key)
	}
	bin, err := b.get(assetURL, herdrBinaryMax)
	if err != nil {
		return "", fmt.Errorf("download herdr: %w", err)
	}
	sum := sha256.Sum256(bin)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return "", fmt.Errorf("checksum mismatch (got %s, want %s)", got, want)
	}
	home, err := b.home()
	if err != nil {
		return "", err
	}
	dest := localHerdrPath(home)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "herdr-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func httpGetBytes(rawURL string, max int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Identity encoding: we hash the body, so a transparent gzip decode would
	// make the digest disagree with the published SHA-256 of the file.
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Timeout: herdrHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}
