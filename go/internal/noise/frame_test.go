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

func TestDecodeEmptyFrameErrors(t *testing.T) {
	if _, _, err := DecodeFrame(nil); err == nil {
		t.Fatal("expected error decoding empty frame")
	}
}
