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
