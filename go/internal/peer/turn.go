package peer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type turnCreds struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	URLs     []string `json:"urls"`
}

// FetchTURN asks the signaling server for ephemeral TURN credentials and returns
// them as ICE servers. Returns (nil, nil) when TURN isn't configured (the
// endpoint 404s) so callers fall back to STUN-only without error.
func FetchTURN(ctx context.Context, signalURL string) ([]ICEServer, error) {
	url := strings.TrimRight(signalURL, "/") + "/turn-credentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil // TURN not configured -> STUN-only
	}
	var tc turnCreds
	if err := json.NewDecoder(resp.Body).Decode(&tc); err != nil {
		return nil, err
	}
	if len(tc.URLs) == 0 {
		return nil, nil
	}
	return []ICEServer{{URLs: tc.URLs, Username: tc.Username, Credential: tc.Password}}, nil
}
