// go/internal/client/lastused.go — remembers the machine the user attached to
// last, so a bare `mir attach` (and the overview's initial cursor) can mean
// "continue where I left off". Convenience state only: a missing or unreadable
// file simply means no default.
package client

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func lastUsedPath(dir string) string { return filepath.Join(dir, "lastused.json") }

type lastUsed struct {
	Name string `json:"name"`
}

// SaveLastUsed records name as the most recently attached machine. Best-effort:
// failures cost the convenience, never the attach.
func SaveLastUsed(dir, name string) {
	if name == "" {
		return
	}
	data, err := json.Marshal(lastUsed{Name: name})
	if err != nil {
		return
	}
	_ = os.WriteFile(lastUsedPath(dir), data, 0o600)
}

// LastUsed returns the most recently attached machine's name, "" when unknown.
func LastUsed(dir string) string {
	data, err := os.ReadFile(lastUsedPath(dir))
	if err != nil {
		return ""
	}
	var l lastUsed
	if err := json.Unmarshal(data, &l); err != nil {
		return ""
	}
	return l.Name
}
