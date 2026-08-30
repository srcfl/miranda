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
	// Channel records which channel wrote the cache ("stable" or "pre"). A
	// cache written on the other channel is ignored, so a stable build never
	// nags about a beta tag it would then refuse to install, and a beta build
	// does not trust a stale stable answer. Empty (pre-existing caches) reads
	// as "stable".
	Channel string `json:"channel,omitempty"`
}

func (c *Client) channel() string {
	if c.Pre {
		return "pre"
	}
	return "stable"
}

func (c *Client) shouldCheck(cachePath string, window time.Duration) bool {
	st, err := readCheck(cachePath)
	if err != nil {
		return true // no/invalid cache => check
	}
	if channelOf(st) != c.channel() {
		return true // cache from the other channel => check
	}
	return time.Since(st.CheckedAt) >= window
}

func channelOf(st *checkState) string {
	if st.Channel == "" {
		return "stable"
	}
	return st.Channel
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

func (c *Client) writeCheck(cachePath, latestTag string, at time.Time) error {
	b, err := json.Marshal(checkState{LatestTag: latestTag, CheckedAt: at, Channel: c.channel()})
	if err != nil {
		return err
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath)
}

func (c *Client) cachedLatest(cachePath string) string {
	st, err := readCheck(cachePath)
	if err != nil || channelOf(st) != c.channel() {
		return ""
	}
	return st.LatestTag
}

// notify prints the one-line update notice.
func (c *Client) notify(w io.Writer, currentVersion, tag string) {
	fmt.Fprintf(w, "Update available: %s → %s   run: %s update\n", currentVersion, tag, c.Binary)
}

// MaybeNotify surfaces an "update available" line to w (stderr) without ever
// delaying the command, per the spec's "run in the background so it never delays
// a command". It does two independent things:
//
//   - Foreground (instant, no network): if the on-disk cache already knows of a
//     newer release on this channel, print the one-line notice now. This is the
//     only thing the user sees.
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
	if tag := c.cachedLatest(cachePath); tag != "" && IsNewer(currentVersion, tag) {
		c.notify(w, currentVersion, tag)
	}
	// Backgrounded refresh when stale; never blocks the caller.
	if c.shouldCheck(cachePath, window) {
		go func() {
			rel, err := c.Latest()
			if err != nil {
				return // silent
			}
			_ = c.writeCheck(cachePath, rel.Tag, time.Now())
		}()
	}
}

// NoticeNow is MaybeNotify's foreground twin for the no-argument guide: the one
// place a user explicitly stops to look around, so a bounded network check is
// worth a moment. Fresh same-channel cache answers instantly; otherwise one
// request runs with `timeout` as a hard budget and the result is cached. Every
// failure is silent, and MIR_NO_UPDATE_CHECK=1 disables it entirely.
func (c *Client) NoticeNow(w io.Writer, cachePath, currentVersion string, window, timeout time.Duration) {
	if os.Getenv("MIR_NO_UPDATE_CHECK") == "1" {
		return
	}
	if !c.shouldCheck(cachePath, window) {
		if tag := c.cachedLatest(cachePath); tag != "" && IsNewer(currentVersion, tag) {
			c.notify(w, currentVersion, tag)
		}
		return
	}
	bounded := *c
	if c.HTTP != nil {
		hc := *c.HTTP
		hc.Timeout = timeout
		bounded.HTTP = &hc
	}
	rel, err := bounded.Latest()
	if err != nil {
		return // silent — the guide must render regardless
	}
	_ = c.writeCheck(cachePath, rel.Tag, time.Now())
	if IsNewer(currentVersion, rel.Tag) {
		c.notify(w, currentVersion, rel.Tag)
	}
}
