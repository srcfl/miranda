// Package defaults holds the baked-in "it just works" endpoints. The signaling
// relay and STUN server default to ours, so no flags are needed for the common
// case; override with --signal/--stun or the TR_SIGNAL/TR_STUN env vars (e.g. to
// point at a local dev relay or your own infrastructure).
package defaults

import "os"

const (
	// Signal is our hosted signaling server (Cloudflare-fronted, TLS).
	Signal = "https://relay.sourceful-labs.net"
	// STUN is the default STUN server for NAT traversal (srflx discovery).
	STUN = "stun:stun.l.google.com:19302"
)

// SignalURL returns the effective signaling URL: TR_SIGNAL env, else the baked default.
func SignalURL() string {
	if v := os.Getenv("TR_SIGNAL"); v != "" {
		return v
	}
	return Signal
}

// STUNURL returns the effective STUN URL: TR_STUN env, else the baked default.
func STUNURL() string {
	if v := os.Getenv("TR_STUN"); v != "" {
		return v
	}
	return STUN
}
