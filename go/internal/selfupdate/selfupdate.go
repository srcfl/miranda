// Package selfupdate resolves the latest GitHub Release for a binary and
// (in apply.go) swaps the running executable after SHA256 verification.
// It talks to GitHub directly — never through the relay.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Client describes one binary's update channel. Fields default via New().
type Client struct {
	APIBase string // e.g. https://api.github.com (override in tests)
	Repo    string // "srcfl/miranda"
	Binary  string // "mir" | "mir-agent"
	OS      string // runtime.GOOS
	Arch    string // runtime.GOARCH
	HTTP    *http.Client
	// Pre widens resolution to prereleases (the beta channel). GitHub's
	// /releases/latest NEVER returns a prerelease, so a beta build following
	// that endpoint would report "already up to date" forever. Pre only changes
	// WHICH release is resolved — verification (SHA256 + cosign in Apply) is
	// identical on both channels.
	Pre bool
}

// New builds a Client for the current platform with sane defaults.
func New(repo, binary string) *Client {
	return &Client{
		APIBase: "https://api.github.com",
		Repo:    repo,
		Binary:  binary,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Release is the resolved latest release for this Client's platform.
type Release struct {
	Tag          string
	AssetURL     string // archive for this binary/os/arch
	AssetName    string // archive filename (matched against checksums.txt)
	ChecksumsURL string
	// Cosign keyless signing artifacts for checksums.txt. Both stay empty for
	// releases cut before signing existed; Apply() degrades gracefully in that
	// case (and whenever cosign is not installed locally).
	ChecksumsSigURL  string // checksums.txt.sig  (signature)
	ChecksumsCertURL string // checksums.txt.pem  (Fulcio signing cert)
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName is the archive filename GoReleaser produces for this platform.
func (c *Client) assetName(tag string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", c.Binary, strings.TrimPrefix(tag, "v"), c.OS, c.Arch)
}

// Latest fetches and resolves the most recent release on this Client's
// channel: /releases/latest for stable, the highest semver in the release list
// (prereleases included) when Pre is set.
func (c *Client) Latest() (*Release, error) {
	if c.Pre {
		return c.latestIncludingPre()
	}
	var gr ghRelease
	if err := c.getJSON(fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBase, "/"), c.Repo), &gr); err != nil {
		return nil, err
	}
	return c.resolve(gr)
}

// latestIncludingPre picks the highest semver tag out of the recent release
// list — by version, not list position, so a stable release cut after a beta
// still wins when it compares higher.
func (c *Client) latestIncludingPre() (*Release, error) {
	var grs []ghRelease
	if err := c.getJSON(fmt.Sprintf("%s/repos/%s/releases?per_page=30", strings.TrimRight(c.APIBase, "/"), c.Repo), &grs); err != nil {
		return nil, err
	}
	best := -1
	for i, gr := range grs {
		if gr.Draft || !semver.IsValid(canon(gr.TagName)) {
			continue
		}
		if best < 0 || semver.Compare(canon(gr.TagName), canon(grs[best].TagName)) > 0 {
			best = i
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("no semver-tagged release found for %s", c.Repo)
	}
	return c.resolve(grs[best])
}

func (c *Client) getJSON(url string, v any) error {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github releases: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// resolve matches this platform's asset plus the checksum artifacts in gr.
func (c *Client) resolve(gr ghRelease) (*Release, error) {
	want := c.assetName(gr.TagName)
	rel := &Release{Tag: gr.TagName, AssetName: want}
	for _, a := range gr.Assets {
		switch a.Name {
		case want:
			rel.AssetURL = a.URL
		case "checksums.txt":
			rel.ChecksumsURL = a.URL
		case "checksums.txt.sig":
			rel.ChecksumsSigURL = a.URL
		case "checksums.txt.pem":
			rel.ChecksumsCertURL = a.URL
		}
	}
	if rel.AssetURL == "" {
		return nil, fmt.Errorf("no asset %q in release %s", want, gr.TagName)
	}
	if rel.ChecksumsURL == "" {
		return nil, fmt.Errorf("no checksums.txt in release %s", gr.TagName)
	}
	return rel, nil
}

// IsPrerelease reports whether v (with or without leading v) is a semver
// prerelease, e.g. 0.8.0-beta.1. It decides the default update channel: a
// build that IS a prerelease follows prereleases.
func IsPrerelease(v string) bool {
	cv := canon(v)
	return semver.IsValid(cv) && semver.Prerelease(cv) != ""
}

// IsNewer reports whether latest (a tag, with or without leading v) is a higher
// semver than cur. A non-semver cur (e.g. "dev") is always treated as older.
func IsNewer(cur, latest string) bool {
	c := canon(cur)
	l := canon(latest)
	if !semver.IsValid(c) {
		return true
	}
	if !semver.IsValid(l) {
		return false
	}
	return semver.Compare(l, c) > 0
}

func canon(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
