// go/internal/noise/noise_test.go
package noise

import (
	"bytes"
	"testing"
)

func TestKKHandshakeAndTransport(t *testing.T) {
	iPriv, iPub, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	rPriv, rPub, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}

	initiator, err := NewInitiator(iPriv, rPub)
	if err != nil {
		t.Fatal(err)
	}
	responder, err := NewResponder(rPriv, iPub)
	if err != nil {
		t.Fatal(err)
	}

	// Message 1: initiator -> responder
	msg0, err := initiator.WriteMessage([]byte("hello-payload"))
	if err != nil {
		t.Fatal(err)
	}
	got0, err := responder.ReadMessage(msg0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got0) != "hello-payload" {
		t.Fatalf("payload0 mismatch: %q", got0)
	}

	// Message 2: responder -> initiator
	msg1, err := responder.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.ReadMessage(msg1); err != nil {
		t.Fatal(err)
	}

	if !initiator.Done() || !responder.Done() {
		t.Fatal("handshake not complete")
	}

	// Transport: both directions
	is := initiator.Session()
	rs := responder.Session()

	ct, err := is.Encrypt([]byte("i->r"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rs.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, []byte("i->r")) {
		t.Fatalf("i->r transport mismatch: %q", pt)
	}

	ct2, err := rs.Encrypt([]byte("r->i"))
	if err != nil {
		t.Fatal(err)
	}
	pt2, err := is.Decrypt(ct2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt2, []byte("r->i")) {
		t.Fatalf("r->i transport mismatch: %q", pt2)
	}
}

func TestKKRejectsWrongPeer(t *testing.T) {
	iPriv, iPub, _ := GenerateStatic()
	rPriv, _, _ := GenerateStatic()
	_, wrongPub, _ := GenerateStatic() // not the responder

	initiator, err := NewInitiator(iPriv, wrongPub) // initiator targets the wrong peer
	if err != nil {
		t.Fatal(err)
	}
	responder, err := NewResponder(rPriv, iPub)
	if err != nil {
		t.Fatal(err)
	}
	msg0, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(msg0); err == nil {
		t.Fatal("expected handshake failure with mismatched static keys")
	}
}
