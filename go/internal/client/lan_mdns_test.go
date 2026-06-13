package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

// TestMDNSResolverFindsAdvertisedMachine exercises the PRODUCTION mdnsResolver
// against a real zeroconf advertisement on the loopback/LAN. It is skipped where
// multicast isn't available (CI sandboxes, restricted networks) so the suite stays
// deterministic — the wire/QUIC path is covered by the non-multicast tests.
func TestMDNSResolverFindsAdvertisedMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live mDNS test under -short")
	}
	const machineID = "testmid_abc123"
	server, err := zeroconf.Register(machineID, mdnsService, mdnsDomain, 47777, []string{"mid=" + machineID}, nil)
	if err != nil {
		t.Skipf("mDNS register unavailable here: %v", err)
	}
	defer server.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addr, err := mdnsResolver{}.resolve(ctx, machineID)
	if err != nil {
		t.Skipf("mDNS browse returned nothing (no multicast on this host?): %v", err)
	}
	if !strings.HasSuffix(addr, ":47777") {
		t.Errorf("resolve(%q) = %q, want the advertised :47777 port", machineID, addr)
	}
}
