// Package defaults holds the baked-in "it just works" endpoints. The signaling
// relay and STUN server default to ours, so no flags are needed for the common
// case; override with --signal/--stun or the MIR_SIGNAL/MIR_STUN env vars (e.g. to
// point at a local dev relay or your own infrastructure).
package defaults

import "os"

const (
	// Signal is our hosted signaling server (Cloudflare-fronted, TLS).
	Signal = "https://relay.sourceful-labs.net"
	// STUN is the default STUN server for NAT traversal (srflx discovery).
	STUN = "stun:stun.l.google.com:19302"
	// Web is where the browser SPA is hosted. Pairing QR codes encode
	// Web + "/#" + code so scanning with a phone opens the SPA ready to pair.
	Web = "https://term.sourceful-labs.net"
)

// SignalURL returns the effective signaling URL: MIR_SIGNAL env, else the baked default.
func SignalURL() string {
	if v := os.Getenv("MIR_SIGNAL"); v != "" {
		return v
	}
	return Signal
}

// STUNURL returns the effective STUN URL: MIR_STUN env, else the baked default.
func STUNURL() string {
	if v := os.Getenv("MIR_STUN"); v != "" {
		return v
	}
	return STUN
}

// WebURL returns the effective SPA base URL: MIR_WEB env, else the baked default.
func WebURL() string {
	if v := os.Getenv("MIR_WEB"); v != "" {
		return v
	}
	return Web
}
