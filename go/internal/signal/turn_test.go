package signal

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestTURNCredentials(t *testing.T) {
	s := New()

	// Unconfigured -> 404 (clients fall back to STUN-only).
	resp := serveTURN(t, s)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured: want 404, got %d", resp.StatusCode)
	}

	// Configured -> ephemeral creds via the coturn REST-API scheme.
	s.TURNSecret = "shared-with-coturn"
	s.TURNURL = "turn:relay.example:3478"
	resp2 := serveTURN(t, s)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("configured: want 200, got %d", resp2.StatusCode)
	}
	if resp2.Header.Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("TURN credentials must not be CORS-*")
	}
	spa := httptest.NewRequest(http.MethodGet, "/turn-credentials", nil)
	spa.Header.Set("Origin", "https://term.sourceful-labs.net")
	spaRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(spaRR, spa)
	if got := spaRR.Header().Get("Access-Control-Allow-Origin"); got != "https://term.sourceful-labs.net" {
		t.Fatalf("SPA Origin ACAO = %q, want the Miranda web origin", got)
	}
	if spaRR.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatal("SPA CORS must not be *")
	}
	evil := httptest.NewRequest(http.MethodGet, "/turn-credentials", nil)
	evil.Header.Set("Origin", "https://evil.example")
	evilRR := httptest.NewRecorder()
	s.Handler().ServeHTTP(evilRR, evil)
	if got := evilRR.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted Origin ACAO = %q, want empty", got)
	}
	var c TURNCreds
	if err := json.NewDecoder(resp2.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if turnTTL > 15*time.Minute {
		t.Fatalf("turnTTL: want at most 15m, got %v", turnTTL)
	}
	if c.TTL != int(turnTTL.Seconds()) {
		t.Fatalf("json ttl: want %d, got %d", int(turnTTL.Seconds()), c.TTL)
	}
	// username = a future unix expiry
	exp, err := strconv.ParseInt(c.Username, 10, 64)
	if err != nil || exp <= time.Now().Unix() {
		t.Fatalf("username should be a future expiry, got %q", c.Username)
	}
	expiry := time.Unix(exp, 0)
	if expiry.Before(time.Now().Add(turnTTL-time.Minute)) || expiry.After(time.Now().Add(turnTTL+time.Minute)) {
		t.Fatalf("username expiry should be ~turnTTL (%v) out, got %s", turnTTL, expiry)
	}
	// password = base64(HMAC-SHA1(secret, username)) — what coturn will verify
	mac := hmac.New(sha1.New, []byte("shared-with-coturn"))
	mac.Write([]byte(c.Username))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); c.Password != want {
		t.Fatalf("password HMAC mismatch")
	}
	if len(c.URLs) != 1 || c.URLs[0] != "turn:relay.example:3478" {
		t.Fatalf("unexpected urls: %v", c.URLs)
	}
}

func serveTURN(t *testing.T, s *Server) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/turn-credentials", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr.Result()
}
