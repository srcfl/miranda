package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerSetsTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(":0", handler)

	if srv.Addr != ":0" {
		t.Fatalf("addr: want :0, got %q", srv.Addr)
	}
	if srv.Handler != handler {
		t.Fatal("handler was not preserved")
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout: got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout: got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 15*time.Second {
		t.Fatalf("WriteTimeout: got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout: got %v", srv.IdleTimeout)
	}
}
