// go/cmd/tr-signal/main.go
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/srcful/terminal-relay/go/internal/signal"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address (TLS terminated by the fronting proxy)")
	flag.Parse()

	s := signal.New()
	srv := &http.Server{Addr: *addr, Handler: s.Handler()}
	log.Printf("tr-signal listening on %s (signaling only; no terminal data)", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
