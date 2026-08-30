package peer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type turnCreds struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	TTL      int      `json:"ttl"`
	URLs     []string `json:"urls"`
}

// TURNResult is a minted TURN credential plus the moment it stops being valid,
// so a caller can reuse it until shortly before it dies instead of asking the
// relay again on every attach. Servers is empty (and Expiry zero) when the relay
// offers no TURN.
type TURNResult struct {
	Servers []ICEServer
	Expiry  time.Time
}

// FetchTURN asks the signaling server for ephemeral TURN credentials and returns
// them as ICE servers. Returns (nil, nil) when TURN isn't configured (the
// endpoint 404s) so callers fall back to STUN-only without error.
func FetchTURN(ctx context.Context, signalURL string) ([]ICEServer, error) {
	res, err := FetchTURNCreds(ctx, nil, signalURL)
	return res.Servers, err
}

// FetchTURNCreds is FetchTURN with the credential's expiry and an injectable
// HTTP client. The expiry is the relay's own: it mints coturn REST credentials
// whose username IS the unix expiry coturn validates against, so we read that
// rather than guess. A response without a usable expiry is returned uncached
// (zero Expiry), which callers treat as "ask again next time".
func FetchTURNCreds(ctx context.Context, hc *http.Client, signalURL string) (TURNResult, error) {
	url := strings.TrimRight(signalURL, "/") + "/turn-credentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return TURNResult{}, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return TURNResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TURNResult{}, nil // TURN not configured -> STUN-only
	}
	var tc turnCreds
	if err := json.NewDecoder(resp.Body).Decode(&tc); err != nil {
		return TURNResult{}, err
	}
	if len(tc.URLs) == 0 {
		return TURNResult{}, nil
	}
	return TURNResult{
		Servers: []ICEServer{{URLs: tc.URLs, Username: tc.Username, Credential: tc.Password}},
		Expiry:  turnExpiry(tc, time.Now()),
	}, nil
}

// turnExpiry prefers the expiry embedded in the username (what coturn checks),
// falling back to the advertised ttl. Zero means "unknown — do not cache".
func turnExpiry(tc turnCreds, now time.Time) time.Time {
	if unix, err := strconv.ParseInt(tc.Username, 10, 64); err == nil && unix > 0 {
		return time.Unix(unix, 0)
	}
	if tc.TTL > 0 {
		return now.Add(time.Duration(tc.TTL) * time.Second)
	}
	return time.Time{}
}
