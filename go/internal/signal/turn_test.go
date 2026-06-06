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
	if resp2.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS header (browser fetch is cross-origin)")
	}
	var c TURNCreds
	if err := json.NewDecoder(resp2.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if turnTTL != 12*time.Hour {
		t.Fatalf("turnTTL: want 12h (must outlast a session), got %v", turnTTL)
	}
	if c.TTL != int((10 * time.Minute).Seconds()) {
		t.Fatalf("json ttl: want 600, got %d", c.TTL)
	}
	// username = a future unix expiry
	exp, err := strconv.ParseInt(c.Username, 10, 64)
	if err != nil || exp <= time.Now().Unix() {
		t.Fatalf("username should be a future expiry, got %q", c.Username)
	}
	expiry := time.Unix(exp, 0)
	if expiry.Before(time.Now().Add(9*time.Minute)) || expiry.After(time.Now().Add(11*time.Minute)) {
		t.Fatalf("username expiry should be about 10m out, got %s", expiry)
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
