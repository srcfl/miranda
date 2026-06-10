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
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName is the archive filename GoReleaser produces for this platform.
func (c *Client) assetName(tag string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", c.Binary, strings.TrimPrefix(tag, "v"), c.OS, c.Arch)
}

// Latest fetches and resolves the most recent release.
func (c *Client) Latest() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBase, "/"), c.Repo)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases: %s", resp.Status)
	}
	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, err
	}
	want := c.assetName(gr.TagName)
	rel := &Release{Tag: gr.TagName, AssetName: want}
	for _, a := range gr.Assets {
		switch a.Name {
		case want:
			rel.AssetURL = a.URL
		case "checksums.txt":
			rel.ChecksumsURL = a.URL
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
