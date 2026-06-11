// go/internal/pairing/interop_test.go
package pairing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/flynn/noise"

	"github.com/srcful/terminal-relay/go/internal/sas"
)

var (
	fxToken   = mustHex("00112233445566778899aabbccddeeff") // 16-byte token
	fxInitEph = mustHex("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	fxRespEph = mustHex("2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40")
	fxOwner   = mustHex("a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf") // owner pub
	fxInfo    = `{"host_pub":"5051525354555657585950515253545550515253545556575859505152535455","machine_id":"m42","name":"box"}`
)

func mustHex(s string) []byte { b, _ := hex.DecodeString(s); return b }

type fixedReader struct {
	data []byte
	pos  int
}

func (r *fixedReader) Read(p []byte) (int, error) {
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

type pairVectors struct {
	Token     string `json:"token"`
	OwnerPub  string `json:"owner_pub"`
	InfoJSON  string `json:"info_json"`
	RoomID    string `json:"room_id"`
	PSK       string `json:"psk"`
	Msg1      string `json:"msg1"`
	Msg2      string `json:"msg2"`
	SafetyNum string `json:"safety_number"`
}

func nnpsk0(initiator bool) *noise.HandshakeState {
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
	eph := fxInitEph
	if !initiator {
		eph = fxRespEph
	}
	hs, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite: cs, Pattern: noise.HandshakeNN, Initiator: initiator,
		Prologue:     []byte("terminal-relay/pair/v1"),
		PresharedKey: pskFromToken(fxToken), PresharedKeyPlacement: 0,
		Random: &fixedReader{data: eph},
	})
	return hs
}

func runFixed(t *testing.T) pairVectors {
	t.Helper()
	ini := nnpsk0(true)
	res := nnpsk0(false)
	msg1, _, _, err := ini.WriteMessage(nil, fxOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := res.ReadMessage(nil, msg1); err != nil {
		t.Fatal(err)
	}
	msg2, _, _, err := res.WriteMessage(nil, []byte(fxInfo))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ini.ReadMessage(nil, msg2); err != nil {
		t.Fatal(err)
	}
	psk := sha256.Sum256(append([]byte("terminal-relay/pair/psk"), fxToken...))
	return pairVectors{
		Token: hex.EncodeToString(fxToken), OwnerPub: hex.EncodeToString(fxOwner),
		InfoJSON: fxInfo, RoomID: RoomID(fxToken), PSK: hex.EncodeToString(psk[:]),
		Msg1: hex.EncodeToString(msg1), Msg2: hex.EncodeToString(msg2),
		SafetyNum: sas.FromBinding(ini.ChannelBinding()),
	}
}

func TestPairInteropVectorsStable(t *testing.T) {
	v := runFixed(t)
	path := filepath.Join("..", "..", "..", "testdata", "pair-interop.json")
	if os.Getenv("UPDATE_VECTORS") == "1" {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		data, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("pair vectors written")
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors (run UPDATE_VECTORS=1 first): %v", err)
	}
	var want pairVectors
	_ = json.Unmarshal(raw, &want)
	if v.Msg1 != want.Msg1 || v.Msg2 != want.Msg2 || v.SafetyNum != want.SafetyNum {
		t.Fatalf("Go pairing drifted from committed vectors")
	}
	_ = io.Discard
	_ = bytes.Equal
}
