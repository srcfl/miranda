package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type checkState struct {
	LatestTag string    `json:"latest_tag"`
	CheckedAt time.Time `json:"checked_at"`
}

func shouldCheck(cachePath string, window time.Duration) bool {
	st, err := readCheck(cachePath)
	if err != nil {
		return true // no/invalid cache => check
	}
	return time.Since(st.CheckedAt) >= window
}

func readCheck(cachePath string) (*checkState, error) {
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st checkState
	if err := json.NewDecoder(f).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeCheck(cachePath, latestTag string, at time.Time) error {
	b, err := json.Marshal(checkState{LatestTag: latestTag, CheckedAt: at})
	if err != nil {
		return err
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath)
}

func cachedLatest(cachePath string) string {
	st, err := readCheck(cachePath)
	if err != nil {
		return ""
	}
	return st.LatestTag
}

// MaybeNotify surfaces an "update available" line to w (stderr) without ever
// delaying the command, per the spec's "run in the background so it never delays
// a command". It does two independent things:
//
//   - Foreground (instant, no network): if the on-disk cache already knows of a
//     newer release, print the one-line notice now. This is the only thing the
//     user sees.
//   - Background (best-effort): when the cache is older than `window`, refresh it
//     in a goroutine. The result is shown on a LATER run — a short-lived command
//     (e.g. `mir list`) may exit before the refresh lands, which is fine; a
//     long-lived one (`mir attach`, `mir up`) keeps the cache fresh.
//
// All failures are silent so normal output is never disrupted. currentVersion is
// the running binary's version (e.g. "0.1.0" or "dev"). Disabled entirely by
// MIR_NO_UPDATE_CHECK=1.
func (c *Client) MaybeNotify(w io.Writer, cachePath, currentVersion string, window time.Duration) {
	if os.Getenv("MIR_NO_UPDATE_CHECK") == "1" {
		return
	}
	// Instant, cache-only notice — never touches the network.
	if tag := cachedLatest(cachePath); tag != "" && IsNewer(currentVersion, tag) {
		fmt.Fprintf(w, "Update available: %s → %s   run: %s self-update\n", currentVersion, tag, c.Binary)
	}
	// Backgrounded refresh when stale; never blocks the caller.
	if shouldCheck(cachePath, window) {
		go func() {
			rel, err := c.Latest()
			if err != nil {
				return // silent
			}
			_ = writeCheck(cachePath, rel.Tag, time.Now())
		}()
	}
}
