package signal

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Browsers send an Origin header; coder/websocket rejects cross-origin by
// default (403). The relay must accept it (a browser on term.sourceful-labs.net
// connecting to relay.sourceful-labs.net is cross-origin). Regression for the
// acceptOpts (OriginPatterns: ["*"]) on every Accept.
func TestAcceptsCrossOriginWebSocket(t *testing.T) {
	srv := httptest.NewServer(New().Handler())
	defer srv.Close()

	for _, path := range []string{"/attach?owner_id=o&machine_id=m", "/pair?room=deadbeef"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set("Origin", "http://evil.example.com") // mismatched on purpose

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("%s: cross-origin WS rejected: got %d, want 101 (switching protocols)", path, resp.StatusCode)
		}
	}
}
