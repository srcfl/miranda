package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// Apply downloads the release archive, verifies its checksum, extracts the
// binary, and atomically replaces targetPath. targetPath should be the absolute
// path of the currently running executable (see os.Executable).
func (c *Client) Apply(rel *Release, targetPath string) error {
	archive, err := c.fetch(rel.AssetURL)
	if err != nil {
		return fmt.Errorf("download asset: %w", err)
	}
	sums, err := c.fetch(rel.ChecksumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyChecksum(archive, rel.AssetName, sums); err != nil {
		return err
	}
	binData, err := extractBinary(archive, c.Binary)
	if err != nil {
		return err
	}
	return swap(targetPath, binData)
}

func (c *Client) fetch(url string) ([]byte, error) {
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// extractBinary pulls the entry named `name` out of a tar.gz.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// swap writes data to a temp file in target's directory then atomically renames
// it over target. Safe on Unix even while target is the running process.
func swap(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".mir-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// ReExec replaces the current process image with the binary at path, preserving
// PID/FDs (so a systemd/supervisor wrapper survives). Unix only.
func ReExec(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}
