// go/internal/agent/store.go
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// Config is the agent's persisted identity + settings, stored as config.json.
type Config struct {
	HostPrivHex  string   `json:"host_priv"`     // X25519 host private key (hex)
	HostPubHex   string   `json:"host_pub"`      // derived; convenience
	MachineID    string   `json:"machine_id"`    // random, stable
	MachineName  string   `json:"machine_name"`  // human label (travels E2E only)
	SignalURL    string   `json:"signal_url"`    // e.g. http://localhost:8443
	PairedOwners []string `json:"paired_owners"` // hex owner pubkeys
}

func configPath(dir string) string { return filepath.Join(dir, "config.json") }

// LoadOrInit reads config.json from dir, creating a fresh host key + machine id
// on first use. machineName/signalURL update the stored values.
func LoadOrInit(dir, machineName, signalURL string) (*Config, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll is a no-op on an existing dir and does not tighten its mode, so
	// defensively chmod regardless of whether it pre-existed. The config holds the
	// secret host private key and must never be group/world readable.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	cfg := &Config{}
	if data, err := os.ReadFile(configPath(dir)); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}
	if cfg.HostPrivHex == "" {
		priv, pub, err := noise.GenerateStatic()
		if err != nil {
			return nil, err
		}
		cfg.HostPrivHex = hex.EncodeToString(priv)
		cfg.HostPubHex = hex.EncodeToString(pub)
	}
	if cfg.MachineID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		cfg.MachineID = hex.EncodeToString(b)
	}
	cfg.MachineName = machineName
	cfg.SignalURL = signalURL
	if err := save(dir, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func save(dir string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := configPath(dir)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// WriteFile preserves the mode of a pre-existing file (it only truncates), so
	// explicitly tighten to 0600 to protect the host private key.
	return os.Chmod(path, 0o600)
}

// PinOwner adds an owner pubkey (hex) to the trusted set and persists it.
func PinOwner(dir, ownerPubHex string) error {
	cfg := &Config{}
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return err
	}
	if !cfg.IsOwnerPinned(ownerPubHex) {
		cfg.PairedOwners = append(cfg.PairedOwners, ownerPubHex)
	}
	return save(dir, cfg)
}

func (c *Config) IsOwnerPinned(ownerPubHex string) bool {
	for _, o := range c.PairedOwners {
		if o == ownerPubHex {
			return true
		}
	}
	return false
}

func (c *Config) HostPriv() []byte { b, _ := hex.DecodeString(c.HostPrivHex); return b }
func (c *Config) HostPub() []byte  { b, _ := hex.DecodeString(c.HostPubHex); return b }
