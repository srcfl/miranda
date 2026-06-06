package signal

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// TURN credential TTL handed to clients (coturn validates the embedded expiry).
// turnTTL must outlast a terminal session: coturn re-validates the credential's
// embedded expiry on every allocation refresh / CreatePermission, so too short a
// TTL drops TURN mid-session ("CreatePermission 401" on cross-network/cellular).
// The cred is fetched per-attach and only grants relay bandwidth (Noise keeps
// content E2E; coturn quotas bound abuse), so a generous TTL is acceptable.
const turnTTL = 12 * time.Hour

// TURNCreds is the ephemeral TURN credential issued to a client. It follows the
// coturn "TURN REST API" scheme: username = expiry-unix, password =
// base64(HMAC-SHA1(static-auth-secret, username)). The static secret lives only
// on the server + coturn; clients never see it, only short-lived derived creds.
type TURNCreds struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	TTL      int      `json:"ttl"`
	URLs     []string `json:"urls"`
}

// handleTURN issues an ephemeral TURN credential. 404 when TURN isn't configured
// (clients then fall back to STUN-only). Public + CORS-open: the credential is
// short-lived and grants only relay bandwidth (Noise keeps content E2E).
func (s *Server) handleTURN(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	if s.TURNSecret == "" || s.TURNURL == "" {
		http.Error(w, "turn not configured", http.StatusNotFound)
		return
	}
	username := strconv.FormatInt(time.Now().Add(turnTTL).Unix(), 10)
	mac := hmac.New(sha1.New, []byte(s.TURNSecret))
	mac.Write([]byte(username))
	creds := TURNCreds{
		Username: username,
		Password: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		TTL:      int(turnTTL.Seconds()),
		URLs:     []string{s.TURNURL},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(creds)
}
