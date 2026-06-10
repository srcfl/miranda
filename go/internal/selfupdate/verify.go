package selfupdate

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// verifyChecksum confirms data's SHA256 matches the entry for name in a
// GoReleaser-style checksums.txt ("<hex>  <name>" per line).
func verifyChecksum(data []byte, name string, checksums []byte) error {
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		fields := bytes.Fields(sc.Bytes())
		if len(fields) == 2 && string(fields[1]) == name {
			want = string(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum entry for %s", name)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: have %s want %s", name, got, want)
	}
	return nil
}
