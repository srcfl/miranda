// go/cmd/tr-signal/main.go
package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

func main() {
	addr := flag.String("addr", ":8443", "plain HTTP listen address (front it with a TLS proxy, or also set --tls-*)")
	tlsAddr := flag.String("tls-addr", "", "if set with --tls-cert/--tls-key, also serve HTTPS here (e.g. :443) for Cloudflare Full (strict)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (PEM)")
	tlsKey := flag.String("tls-key", "", "TLS private key file (PEM)")
	webroot := flag.String("webroot", "", "if set, serve the static SPA from this directory on non-signaling paths")
	turnURL := flag.String("turn-url", "", "TURN url to hand out (e.g. turn:relay.example:3478); secret via TR_TURN_SECRET env")
	flag.Parse()

	s := signal.New()
	s.TURNURL = *turnURL
	s.TURNSecret = os.Getenv("TR_TURN_SECRET") // shared with coturn; never logged/shipped
	if s.TURNSecret != "" && s.TURNURL != "" {
		log.Printf("tr-signal: issuing ephemeral TURN credentials for %s", s.TURNURL)
	}
	var handler http.Handler = s.Handler()
	if *webroot != "" {
		handler = withStatic(s.Handler(), *webroot)
		log.Printf("tr-signal serving SPA from %s", *webroot)
	}
	// Serve HTTPS directly when a cert is provided (Cloudflare "Full (strict)":
	// the CF->origin leg is then encrypted). Runs alongside the plain listener so
	// the cutover from "Flexible" (CF->origin :80) has no downtime.
	if *tlsAddr != "" && *tlsCert != "" && *tlsKey != "" {
		go func() {
			ts := newHTTPServer(*tlsAddr, handler)
			log.Printf("tr-signal HTTPS listening on %s", *tlsAddr)
			if err := ts.ListenAndServeTLS(*tlsCert, *tlsKey); err != nil {
				log.Fatal(err)
			}
		}()
	}
	srv := newHTTPServer(*addr, handler)
	log.Printf("tr-signal listening on %s (signaling only; no terminal data)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// withStatic routes the signaling endpoints to the signal server and serves
// everything else (the SPA: index.html, /src, /vendor) from dir. Serving the
// client code does not weaken the data-plane guarantee — terminal bytes still
// flow P2P+Noise — but it does make this host a trust root for the client code
// (see SECURITY.md, "the code you run").
func withStatic(sig http.Handler, dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	indexPath := filepath.Join(dir, "index.html")
	signalPaths := map[string]bool{"/agent/signal": true, "/attach": true, "/pair": true, "/turn-credentials": true, "/healthz": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signalPaths[r.URL.Path] {
			sig.ServeHTTP(w, r)
			return
		}
		// index.html carries an inline import map; serve it with a per-request
		// nonce so script-src can stay 'self' (no 'unsafe-inline').
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			serveIndex(w, indexPath)
			return
		}
		setStaticSecurityHeaders(w, "")
		fs.ServeHTTP(w, r)
	})
}

// serveIndex serves index.html with a fresh per-request nonce on its inline
// import map, and a matching CSP — so the only inline script that can run is our
// own import map, and an injected script can neither execute nor exfiltrate the
// passkey-derived owner key.
func serveIndex(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.StdEncoding.EncodeToString(b)
	setStaticSecurityHeaders(w, nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(string(data), "__CSP_NONCE__", nonce)))
}

func setStaticSecurityHeaders(w http.ResponseWriter, nonce string) {
	// The hosted SPA is a client-code trust root: it derives the owner key and
	// runs the terminal crypto. Keep the browser sandbox tight around our own
	// static files while allowing HTTPS/WSS relay connections and QR camera scan.
	scriptSrc := "script-src 'self'"
	if nonce != "" {
		scriptSrc += " 'nonce-" + nonce + "'" // for the inline import map
	}
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		scriptSrc,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"connect-src 'self' https: wss:",
		"media-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
	}, "; "))
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=(), payment=(), usb=(), serial=()")
	// The SPA currently has unhashed /src and /vendor paths. Prefer freshness over
	// stale trusted-code delivery until a content-hashed build exists.
	w.Header().Set("Cache-Control", "no-store")
}
