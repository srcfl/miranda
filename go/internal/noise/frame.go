// go/internal/noise/frame.go
package noise

import (
	"encoding/binary"
	"errors"
)

// FrameType is the 1-byte tag prefixing every frame inside the Noise channel.
type FrameType byte

const (
	FrameData    FrameType = 0x01 // raw PTY bytes
	FrameResize  FrameType = 0x02 // cols u16 BE ++ rows u16 BE
	FrameHello   FrameType = 0x03 // UTF-8 JSON metadata
	FrameWindows FrameType = 0x04 // UTF-8 JSON tmux window snapshot (agent -> client)
)

// EncodeData wraps raw PTY bytes in a DATA frame.
func EncodeData(b []byte) []byte {
	return append([]byte{byte(FrameData)}, b...)
}

// EncodeResize encodes terminal dimensions as a RESIZE frame.
func EncodeResize(cols, rows uint16) []byte {
	out := make([]byte, 5)
	out[0] = byte(FrameResize)
	binary.BigEndian.PutUint16(out[1:3], cols)
	binary.BigEndian.PutUint16(out[3:5], rows)
	return out
}

// EncodeHello wraps JSON metadata in a HELLO frame.
func EncodeHello(jsonBytes []byte) []byte {
	return append([]byte{byte(FrameHello)}, jsonBytes...)
}

// EncodeWindows wraps a tmux window snapshot (JSON) in a WINDOWS frame.
func EncodeWindows(jsonBytes []byte) []byte {
	return append([]byte{byte(FrameWindows)}, jsonBytes...)
}

// DecodeFrame splits a frame into its type and payload.
func DecodeFrame(b []byte) (FrameType, []byte, error) {
	if len(b) < 1 {
		return 0, nil, errors.New("empty frame")
	}
	return FrameType(b[0]), b[1:], nil
}

// DecodeResize parses a RESIZE payload into cols and rows.
func DecodeResize(payload []byte) (cols, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, errors.New("resize payload must be 4 bytes")
	}
	return binary.BigEndian.Uint16(payload[0:2]), binary.BigEndian.Uint16(payload[2:4]), nil
}
