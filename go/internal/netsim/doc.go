// Package netsim holds the driver for the Docker NAT-simulation harness in
// netsim/. Everything lives in _test.go files, so nothing here is linked into
// mir, mir-agent or mir-signal — the harness is built with `go test -c` and the
// resulting test binary is what runs inside the containers.
//
// Building the driver as a test binary is deliberate: the client owner root is
// only ever stored in the OS keychain, and internal/client accepts the
// MIR_TEST_KEYCHAIN_DIR override solely from a binary whose argv[0] ends in
// ".test". That keeps the production secret-storage rule intact while letting a
// headless Linux container hold an owner identity.
//
// See netsim/README.md for the topology and how to run it.
package netsim
