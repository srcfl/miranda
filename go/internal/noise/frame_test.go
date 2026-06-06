// go/internal/noise/frame_test.go
package noise

import (
	"bytes"
	"testing"
)

func TestDataFrameRoundTrip(t *testing.T) {
	enc := EncodeData([]byte("ls -la\n"))
	typ, payload, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameData {
		t.Fatalf("expected FrameData, got %d", typ)
	}
	if !bytes.Equal(payload, []byte("ls -la\n")) {
		t.Fatalf("payload mismatch: %q", payload)
	}
}

func TestResizeFrameRoundTrip(t *testing.T) {
	enc := EncodeResize(120, 40)
	typ, payload, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameResize {
		t.Fatalf("expected FrameResize, got %d", typ)
	}
	cols, rows, err := DecodeResize(payload)
	if err != nil {
		t.Fatal(err)
	}
	if cols != 120 || rows != 40 {
		t.Fatalf("expected 120x40, got %dx%d", cols, rows)
	}
}

func TestWindowsFrameRoundTrip(t *testing.T) {
	j := []byte(`{"v":1,"active":"@0","win":[{"id":"@0","i":0,"n":"main"}]}`)
	enc := EncodeWindows(j)
	if enc[0] != byte(FrameWindows) {
		t.Fatalf("expected FrameWindows tag 0x04, got %#x", enc[0])
	}
	typ, payload, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameWindows || !bytes.Equal(payload, j) {
		t.Fatalf("windows frame mismatch: type=%d payload=%q", typ, payload)
	}
}

func TestControlFrameRoundTrip(t *testing.T) {
	j := []byte(`{"a":"select-window","t":"@3"}`)
	enc := EncodeControl(j)
	if enc[0] != byte(FrameControl) || byte(FrameControl) != 0x05 {
		t.Fatalf("expected FrameControl tag 0x05, got %#x", enc[0])
	}
	typ, payload, err := DecodeFrame(enc)
	if err != nil || typ != FrameControl || !bytes.Equal(payload, j) {
		t.Fatalf("control frame mismatch")
	}
}

func TestDecodeEmptyFrameErrors(t *testing.T) {
	if _, _, err := DecodeFrame(nil); err == nil {
		t.Fatal("expected error decoding empty frame")
	}
}
