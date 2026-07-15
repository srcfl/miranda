package signal

import "testing"

func FuzzDecodeInboundSignal(f *testing.F) {
	f.Add([]byte(`{"type":"offer","sdp":"v=0"}`))
	f.Add([]byte(`{"type":"registry","registry":"opaque"}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeInboundSignal(data)
	})
}
