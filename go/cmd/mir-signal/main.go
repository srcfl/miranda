// go/cmd/mir-signal/main.go
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/signal"
	"github.com/srcful/terminal-relay/go/internal/version"
)

// stringSlice is a flag.Value that accumulates one value per occurrence, so
// --csp-connect-src can be repeated. It is the systemd-safe alternative to the
// MIR_CSP_CONNECT_SRC env var: an EnvironmentFile mangles a value that starts
// with a single-quoted token (`'self' https://…` arrived as `selfhttps://…` in
// production), whereas repeated flags pass each token through untouched.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, " ") }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println("mir-signal", version.String())
		return
	}
	addr := flag.String("addr", ":8443", "plain HTTP listen address (front it with a TLS proxy, or also set --tls-*)")
	tlsAddr := flag.String("tls-addr", "", "if set with --tls-cert/--tls-key, also serve HTTPS here (e.g. :443) for Cloudflare Full (strict)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (PEM)")
	tlsKey := flag.String("tls-key", "", "TLS private key file (PEM)")
	webroot := flag.String("webroot", "", "if set, serve the static SPA from this directory on non-signaling paths")
	turnURL := flag.String("turn-url", "", "TURN url to hand out (e.g. turn:relay.example:3478); secret via MIR_TURN_SECRET env")
	trustProxyHeaders := flag.Bool("trust-proxy-headers", false, "trust CF-Connecting-IP/X-Forwarded-For for rate limits (only behind a trusted proxy)")
	var trustedProxyCIDRs stringSlice
	flag.Var(&trustedProxyCIDRs, "trusted-proxy-cidr", "CIDR allowed to set client-IP headers (repeatable; required with --trust-proxy-headers)")
	revocationsFile := flag.String("revocations-file", "", "durable signed machine-revocation store (recommended in production)")
	requireTLS := flag.Bool("require-tls", false, "fail startup unless --tls-addr, certificate, and key are present")
	var cspConnect stringSlice
	flag.Var(&cspConnect, "csp-connect-src", "CSP connect-src token (repeatable); systemd-safe alternative to MIR_CSP_CONNECT_SRC. e.g. --csp-connect-src 'self' --csp-connect-src https://relay.example.net")
	flag.Parse()

	// Default to timestamped log.Printf. Lstdflags gives date+time so each relay
	// event line is sortable in production logs; LUTC keeps relay timestamps in
	// UTC to match the agent's logs, so the two sides correlate during an incident.
	log.SetFlags(log.LstdFlags | log.LUTC)

	s := signal.New()
	s.Logf = log.Printf // structured per-event relay lines (register/replace/reject/gone/attach/flap/stats)
	s.TURNURL = *turnURL
	s.TrustProxyHeaders = *trustProxyHeaders
	if *trustProxyHeaders {
		if err := s.SetTrustedProxyCIDRs(trustedProxyCIDRs); err != nil {
			log.Fatalf("mir-signal: trusted proxy configuration: %v", err)
		}
	}
	s.TURNSecret = os.Getenv("MIR_TURN_SECRET") // shared with coturn; never logged/shipped
	if *revocationsFile != "" {
		if err := s.LoadRevocations(*revocationsFile); err != nil {
			log.Fatalf("mir-signal: load revocations: %v", err)
		}
		log.Printf("mir-signal: durable revocations enabled at %s", *revocationsFile)
	} else {
		log.Printf("mir-signal WARNING: revocations are memory-only; set --revocations-file in production")
	}
	if s.TURNSecret != "" && s.TURNURL != "" {
		log.Printf("mir-signal: issuing ephemeral TURN credentials for %s", s.TURNURL)
	}
	if err := validateTLSConfig(*requireTLS, *tlsAddr, *tlsCert, *tlsKey); err != nil {
		log.Fatalf("mir-signal: %v", err)
	}

	// Stash any --csp-connect-src flag tokens so the static handler's per-request
	// CSP picks them up (flag takes precedence over MIR_CSP_CONNECT_SRC). This is
	// process-global config set once at startup, before any handler runs.
	cspConnectFlag = cspConnect

	var handler http.Handler = s.Handler()
	if *webroot != "" {
		handler = withStatic(s.Handler(), *webroot)
		log.Printf("mir-signal serving SPA from %s (connect-src %q)", *webroot, cspConnectSrc())
		// The SPA is a client-code trust root; without an explicit connect-src it
		// can only talk to its own origin. Warn so an operator serving the SPA
		// against a different-origin relay knows why attaches fail.
		if !cspConnectConfigured() {
			log.Printf("mir-signal WARNING: --webroot set but no CSP connect-src configured; defaulting to 'self'. " +
				"If the relay is a different origin, set --csp-connect-src (repeatable) or MIR_CSP_CONNECT_SRC.")
		}
	}

	// Background gauge: event=stats every ~60s so agent churn / proof-store growth
	// is visible. Lives for the process lifetime.
	go s.RunStats(context.Background())

	// Serve HTTPS directly when a cert is provided (Cloudflare "Full (strict)":
	// the CF->origin leg is then encrypted). The plain listener remains useful for
	// localhost health checks and local development; production firewalls keep it
	// unreachable from the public Internet.
	//
	// Only enter the TLS branch when the cert AND key files actually exist. A
	// production unit uses --require-tls and has already failed above if they are
	// absent; optional mode warns and serves HTTP-only for local development.
	if *tlsAddr != "" && *tlsCert != "" && *tlsKey != "" {
		if fileExists(*tlsCert) && fileExists(*tlsKey) {
			go func() {
				ts := newHTTPServer(*tlsAddr, handler)
				log.Printf("mir-signal HTTPS listening on %s", *tlsAddr)
				if err := ts.ListenAndServeTLS(*tlsCert, *tlsKey); err != nil {
					log.Fatal(err)
				}
			}()
		} else {
			log.Printf("mir-signal WARNING: --tls-addr set but cert/key not found (cert=%q key=%q); serving HTTP-only on %s",
				*tlsCert, *tlsKey, *addr)
		}
	}
	srv := newHTTPServer(*addr, handler)
	log.Printf("mir-signal listening on %s (signaling only; no terminal data)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// fileExists reports whether path names an existing file the process can stat.
// Used to gate the TLS branch so a missing cert/key downgrades to HTTP-only
// instead of crash-looping the process.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func validateTLSConfig(required bool, addr, cert, key string) error {
	if !required {
		return nil
	}
	if addr == "" || cert == "" || key == "" {
		return fmt.Errorf("TLS is required but --tls-addr/--tls-cert/--tls-key are incomplete")
	}
	if !fileExists(cert) || !fileExists(key) {
		return fmt.Errorf("TLS is required but certificate or key is unavailable")
	}
	return nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// ReadHeaderTimeout bounds slow-loris header reads (gosec G112). We must
		// NOT set ReadTimeout/WriteTimeout: the signaling + attach connections are
		// long-lived WebSockets, and a whole-request deadline cuts them mid-stream
		// (~15s churn that breaks any attach spanning it). coder/websocket owns its
		// own per-message read/write deadlines.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
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
	// Every path the signal server owns must be forwarded here, or --webroot mode
	// 404s it into the static file server. This list MUST stay in sync with the
	// routes registered in signal.Server.Handler(); main_test.go's
	// TestWithStaticForwardsSignalingPaths guards against drift (it caught /registry
	// going missing in production).
	signalPaths := map[string]bool{"/agent/signal": true, "/attach": true, "/pair": true, "/turn-credentials": true, "/healthz": true, "/registry": true, "/revocations": true}
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
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "server entropy unavailable", http.StatusInternalServerError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(b)
	setStaticSecurityHeaders(w, nonce)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(string(data), "__CSP_NONCE__", nonce)))
}

// cspConnectFlag holds the --csp-connect-src tokens parsed in main(). It is
// process-global config written once before any handler runs, so the per-request
// CSP in setStaticSecurityHeaders can prefer it over the env var without
// threading it through every static-serving function (and keeping withStatic's
// signature stable for callers/tests).
var cspConnectFlag stringSlice

// cspConnectSrc resolves the CSP connect-src allow-list for the SPA. The SPA is a
// client-code trust root (it derives the owner key and runs the terminal crypto),
// so connect-src is the channel that would exfiltrate the owner key if the served
// JS were ever tampered or a same-origin gadget were found. It therefore defaults
// to 'self' only.
//
// Resolution order:
//  1. Repeated --csp-connect-src flag tokens, space-joined. This is the
//     systemd-safe path: an EnvironmentFile mangles a value that starts with a
//     single-quoted token (`'self' https://…` became `selfhttps://…` in
//     production), so the flag passes each token through verbatim.
//  2. MIR_CSP_CONNECT_SRC env var (whole string), e.g.
//     MIR_CSP_CONNECT_SRC="'self' https://relay.example.net wss://relay.example.net"
//  3. 'self' only.
//
// A previous build shipped the wildcard "'self' https: wss:", which let any
// HTTPS/WSS host receive a beacon — defeating the point of CSP here. See
// SECURITY.md and deploy/lightsail/README.md for the hosted-deployment value.
func cspConnectSrc() string {
	if joined := strings.TrimSpace(strings.Join(cspConnectFlag, " ")); joined != "" {
		return joined
	}
	if v := strings.TrimSpace(os.Getenv("MIR_CSP_CONNECT_SRC")); v != "" {
		return v
	}
	return "'self'"
}

// cspConnectConfigured reports whether the operator explicitly supplied a CSP
// connect-src (via --csp-connect-src flag or MIR_CSP_CONNECT_SRC env). Used to
// warn when --webroot is set but the SPA would silently default to 'self' (and
// thus fail to reach a different-origin relay).
func cspConnectConfigured() bool {
	if strings.TrimSpace(strings.Join(cspConnectFlag, " ")) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("MIR_CSP_CONNECT_SRC")) != ""
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
		"connect-src " + cspConnectSrc(),
		"media-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'none'",
		"upgrade-insecure-requests",
	}, "; "))
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// HSTS at the origin (defense in depth; the CDN may also set it). This host is
	// a passkey/crypto trust root, so pin TLS for future visits. No
	// includeSubDomains — this asserts only for the exact host it is served from,
	// so it cannot force HSTS onto sibling subdomains. Browsers ignore it over
	// plain HTTP (e.g. local dev), so it is safe to send unconditionally.
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=(), payment=(), usb=(), serial=()")
	// The SPA currently has unhashed /src and /vendor paths. Prefer freshness over
	// stale trusted-code delivery until a content-hashed build exists.
	w.Header().Set("Cache-Control", "no-store")
}
