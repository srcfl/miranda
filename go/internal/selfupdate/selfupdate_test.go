package selfupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeAPI(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/srcfl/miranda/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestLatestParsesTagAndAsset(t *testing.T) {
	srv := fakeAPI(t, `{
		"tag_name": "v0.2.0",
		"assets": [
			{"name": "mir_0.2.0_linux_amd64.tar.gz", "browser_download_url": "http://x/mir.tgz"},
			{"name": "checksums.txt", "browser_download_url": "http://x/checksums.txt"}
		]
	}`)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}
	rel, err := c.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.2.0" {
		t.Fatalf("tag=%q", rel.Tag)
	}
	if rel.AssetURL == "" || rel.ChecksumsURL == "" {
		t.Fatalf("asset=%q checksums=%q", rel.AssetURL, rel.ChecksumsURL)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"dev", "v0.2.0", true}, // dev always treated as older
	}
	for _, tc := range cases {
		if got := IsNewer(tc.cur, tc.latest); got != tc.want {
			t.Fatalf("IsNewer(%q,%q)=%v want %v", tc.cur, tc.latest, got, tc.want)
		}
	}
}

// TestLatestPreResolvesPrereleases: the beta funnel. /releases/latest never
// returns a prerelease, so a Pre client must resolve from the release LIST and
// pick the highest semver — by version, not list position.
func TestLatestPreResolvesPrereleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/srcfl/miranda/releases":
			// Deliberately out of order: the highest tag is not first.
			_, _ = w.Write([]byte(`[
				{"tag_name": "v0.7.0", "assets": [
					{"name": "mir_0.7.0_linux_amd64.tar.gz", "browser_download_url": "u"},
					{"name": "checksums.txt", "browser_download_url": "u"}]},
				{"tag_name": "v0.8.0-beta.1", "assets": [
					{"name": "mir_0.8.0-beta.1_linux_amd64.tar.gz", "browser_download_url": "u"},
					{"name": "checksums.txt", "browser_download_url": "u"}]},
				{"tag_name": "not-semver", "assets": []},
				{"tag_name": "v0.8.0-beta.2", "draft": true, "assets": []}
			]`))
		case "/repos/srcfl/miranda/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name": "v0.7.0", "assets": [
				{"name": "mir_0.7.0_linux_amd64.tar.gz", "browser_download_url": "u"},
				{"name": "checksums.txt", "browser_download_url": "u"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pre := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client(), Pre: true}
	rel, err := pre.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.8.0-beta.1" {
		t.Fatalf("pre channel resolved %q, want the highest non-draft semver v0.8.0-beta.1", rel.Tag)
	}

	stable := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client()}
	srel, err := stable.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if srel.Tag != "v0.7.0" {
		t.Fatalf("stable channel resolved %q, want v0.7.0", srel.Tag)
	}
}

// TestLatestPrePrefersNewerStable: a stable release cut after a beta outranks
// it (0.8.0 > 0.8.0-beta.1), so a beta build graduates back to stable.
func TestLatestPrePrefersNewerStable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/srcfl/miranda/releases" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name": "v0.8.0", "assets": [
				{"name": "mir_0.8.0_linux_amd64.tar.gz", "browser_download_url": "u"},
				{"name": "checksums.txt", "browser_download_url": "u"}]},
			{"tag_name": "v0.8.0-beta.1", "assets": [
				{"name": "mir_0.8.0-beta.1_linux_amd64.tar.gz", "browser_download_url": "u"},
				{"name": "checksums.txt", "browser_download_url": "u"}]}
		]`))
	}))
	defer srv.Close()

	pre := &Client{APIBase: srv.URL, Repo: "srcfl/miranda", Binary: "mir", OS: "linux", Arch: "amd64", HTTP: srv.Client(), Pre: true}
	rel, err := pre.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.8.0" {
		t.Fatalf("resolved %q, want the newer stable v0.8.0", rel.Tag)
	}
}

func TestIsPrerelease(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.8.0-beta.1", true},
		{"v0.8.0-beta.1", true},
		{"0.8.0-rc.2", true},
		{"0.8.0", false},
		{"v0.7.0", false},
		{"dev", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsPrerelease(c.v); got != c.want {
			t.Errorf("IsPrerelease(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
