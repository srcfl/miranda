package identity

import (
	"encoding/json"
	"testing"
)

func FuzzRevocationJSON(f *testing.F) {
	f.Add([]byte(`{"v":1,"owner_id":"invalid","machine_id":"machine-1","ts":1,"signature":"AA=="}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var record Revocation
		if json.Unmarshal(data, &record) == nil {
			_ = VerifyRevocation(&record)
		}
	})
}
