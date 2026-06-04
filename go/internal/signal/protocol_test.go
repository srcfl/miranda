// go/internal/signal/protocol_test.go
package signal

import "testing"

func TestSignalMsgRoundTrip(t *testing.T) {
	in := SignalMsg{Type: TypeOffer, Session: "s1", SDP: "v=0..."}
	data, err := in.encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSignal(data)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != TypeOffer || out.Session != "s1" || out.SDP != "v=0..." {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := decodeSignal([]byte("not json")); err == nil {
		t.Fatal("expected decode error")
	}
}
