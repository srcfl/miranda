// go/internal/noise/interop_test.go
package noise

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// Fixed inputs — any change here regenerates the vectors (run with -update).
var (
	fxInitStatic = mustHex("a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf")
	fxRespStatic = mustHex("5051525354555657585950515253545550515253545556575859505152535455")
	fxInitEph    = mustHex("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	fxRespEph    = mustHex("2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40")
	fxPayload0   = []byte("pair-request")
	fxTransport  = []byte("terminal-relay")
	fxPrf        = mustHex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

type fixedReader struct {
	data []byte
	pos  int
}

func (r *fixedReader) Read(p []byte) (int, error) {
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

type interopVectors struct {
	InitStaticPriv string `json:"init_static_priv"`
	RespStaticPriv string `json:"resp_static_priv"`
	InitEphPriv    string `json:"init_eph_priv"`
	RespEphPriv    string `json:"resp_eph_priv"`
	Payload0       string `json:"payload0"`
	Transport      string `json:"transport_plaintext"`
	Msg0           string `json:"msg0"`
	Msg1           string `json:"msg1"`
	TransportCT    string `json:"transport_ct"`
}

func runFixedHandshake(t *testing.T) interopVectors {
	t.Helper()
	iPub, _ := PublicFromPrivate(fxInitStatic)
	rPub, _ := PublicFromPrivate(fxRespStatic)

	initiator, err := newHandshake(true, fxInitStatic, rPub, &fixedReader{data: fxInitEph})
	if err != nil {
		t.Fatal(err)
	}
	responder, err := newHandshake(false, fxRespStatic, iPub, &fixedReader{data: fxRespEph})
	if err != nil {
		t.Fatal(err)
	}

	msg0, err := initiator.WriteMessage(fxPayload0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(msg0); err != nil {
		t.Fatal(err)
	}
	msg1, err := responder.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.ReadMessage(msg1); err != nil {
		t.Fatal(err)
	}
	ct, err := initiator.Session().Encrypt(fxTransport)
	if err != nil {
		t.Fatal(err)
	}

	return interopVectors{
		InitStaticPriv: hex.EncodeToString(fxInitStatic),
		RespStaticPriv: hex.EncodeToString(fxRespStatic),
		InitEphPriv:    hex.EncodeToString(fxInitEph),
		RespEphPriv:    hex.EncodeToString(fxRespEph),
		Payload0:       hex.EncodeToString(fxPayload0),
		Transport:      hex.EncodeToString(fxTransport),
		Msg0:           hex.EncodeToString(msg0),
		Msg1:           hex.EncodeToString(msg1),
		TransportCT:    hex.EncodeToString(ct),
	}
}

func vectorsPath(t *testing.T) string {
	t.Helper()
	// internal/noise -> repo root testdata
	return filepath.Join("..", "..", "..", "testdata", "kk-interop.json")
}

func TestInteropVectorsStable(t *testing.T) {
	v := runFixedHandshake(t)
	path := vectorsPath(t)

	if os.Getenv("UPDATE_VECTORS") == "1" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		data, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		// Also write the owner-derivation vector.
		opriv, opub, err := identity.DeriveOwnerKey(fxPrf)
		if err != nil {
			t.Fatal(err)
		}
		ov, _ := json.MarshalIndent(map[string]string{
			"prf_output": hex.EncodeToString(fxPrf),
			"owner_priv": hex.EncodeToString(opriv),
			"owner_pub":  hex.EncodeToString(opub),
		}, "", "  ")
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "owner-derivation.json"), ov, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("vectors written")
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors (run with UPDATE_VECTORS=1 first): %v", err)
	}
	var want interopVectors
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if v.Msg0 != want.Msg0 || v.Msg1 != want.Msg1 || v.TransportCT != want.TransportCT {
		t.Fatalf("Go handshake no longer matches committed vectors\n got msg0=%s\nwant msg0=%s", v.Msg0, want.Msg0)
	}
	if !bytes.Equal(mustHex(v.Msg0), mustHex(want.Msg0)) {
		t.Fatal("msg0 bytes differ")
	}
}
