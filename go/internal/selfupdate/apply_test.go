package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// makeArchive returns a tar.gz containing one entry named `bin` with `payload`.
func makeArchive(t *testing.T, bin string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: bin, Mode: 0o755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("new-binary-bytes")
	sum := sha256.Sum256(data)
	line := fmt.Sprintf("%s  mir_1.0.0_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))
	if err := verifyChecksum(data, "mir_1.0.0_linux_amd64.tar.gz", []byte(line)); err != nil {
		t.Fatalf("good checksum rejected: %v", err)
	}
	bad := "deadbeef  mir_1.0.0_linux_amd64.tar.gz\n"
	if err := verifyChecksum(data, "mir_1.0.0_linux_amd64.tar.gz", []byte(bad)); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestApplyReplacesTarget(t *testing.T) {
	payload := []byte("#!/bin/sh\necho v2\n")
	archive := makeArchive(t, "mir", payload)
	sum := sha256.Sum256(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			_, _ = w.Write(archive)
		case "/checksums":
			fmt.Fprintf(w, "%s  mir_1.0.0_%s_%s.tar.gz\n", hex.EncodeToString(sum[:]), "os", "arch")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "mir")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Binary: "mir", OS: "os", Arch: "arch", HTTP: srv.Client()}
	rel := &Release{Tag: "v1.0.0", AssetName: "mir_1.0.0_os_arch.tar.gz", AssetURL: srv.URL + "/asset", ChecksumsURL: srv.URL + "/checksums"}
	if err := c.Apply(rel, target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, payload) {
		t.Fatalf("target not replaced: %q", got)
	}
}
