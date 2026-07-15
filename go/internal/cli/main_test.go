package cli

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "miranda-cli-test-keychain-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("MIR_TEST_KEYCHAIN_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
