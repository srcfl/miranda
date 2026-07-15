package pairing

import "testing"

func FuzzDecodeCode(f *testing.F) {
	f.Add(EncodeCode("https://relay.example", make([]byte, 16)))
	f.Add("")
	f.Add("not-base64")
	f.Fuzz(func(t *testing.T, code string) {
		_, _, _ = DecodeCode(code)
	})
}
