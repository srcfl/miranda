// go/cmd/tr-signal/main.go
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address (TLS terminated by the fronting proxy)")
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
	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers, so a slow-header client can't tie up a connection indefinitely on
	// this public, unauthenticated listener (the gosec G112 / Slowloris case).
	// Body and write deadlines are left to the WebSocket layer, which manages its
	// own long-lived read/write timeouts per message.
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("tr-signal listening on %s (signaling only; no terminal data)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// withStatic routes the signaling endpoints to the signal server and serves
// everything else (the SPA: index.html, /src, /vendor) from dir. Serving the
// client code does not weaken the data-plane guarantee — terminal bytes still
// flow P2P+Noise — but it does make this host a trust root for the client code
// (see SECURITY.md, "the code you run").
func withStatic(sig http.Handler, dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	signalPaths := map[string]bool{"/agent/signal": true, "/attach": true, "/pair": true, "/turn-credentials": true, "/healthz": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signalPaths[r.URL.Path] {
			sig.ServeHTTP(w, r)
			return
		}
		// The SPA iterates fast; don't let the CDN/browser serve a stale client.
		// (A future release can switch to content-hashed, long-cached assets.)
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	})
}
