// go/internal/agent/store.go
package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// Config is the agent's persisted identity + settings, stored as config.json.
type Config struct {
	HostPrivHex        string   `json:"host_priv"`                     // X25519 host private key (hex)
	HostPubHex         string   `json:"host_pub"`                      // derived; convenience
	MachineID          string   `json:"machine_id"`                    // random, stable
	RegistrationSecret string   `json:"registration_secret,omitempty"` // relay-side registration proof
	MachineName        string   `json:"machine_name"`                  // human label (travels E2E only)
	SignalURL          string   `json:"signal_url"`                    // e.g. http://localhost:8443
	PairedOwners       []string `json:"paired_owners"`                 // base58 Miranda owner ids
	// OwnerRegistry contains opaque, owner-encrypted discovery records keyed by
	// owner id. They are created by the passkey-holding client during pairing.
	// The agent can republish them but cannot decrypt them.
	OwnerRegistry map[string]string `json:"owner_registry,omitempty"`
	// OwnerRegistrationAuth contains owner signatures authorizing this agent's
	// registration-secret commitment. They are public proofs, not owner secrets.
	OwnerRegistrationAuth map[string]string `json:"owner_registration_auth,omitempty"`
	Dir                   string            `json:"-"` // source directory (for hot-reloading owners)
}

// ReloadOwners reads the current paired-owner set from dir's config.json. Used by
// the running agent to pick up newly-paired owners without a restart.
func ReloadOwners(dir string) ([]string, error) {
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return c.PairedOwners, nil
}

func configPath(dir string) string { return filepath.Join(dir, "config.json") }

// LoadOrInit reads config.json from dir, creating a fresh host key, machine id,
// and relay registration proof on first use. machineName/signalURL update the
// stored values.
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
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		cfg.MachineID = hex.EncodeToString(b)
	}
	if cfg.RegistrationSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		cfg.RegistrationSecret = hex.EncodeToString(b)
	}
	cfg.MachineName = machineName
	cfg.SignalURL = signalURL
	if err := save(dir, cfg); err != nil {
		return nil, err
	}
	cfg.Dir = dir
	return cfg, nil
}

func save(dir string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replacement avoids truncating the machine key or owner pin set if
	// the process crashes during a write.
	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, configPath(dir))
}

// PinOwner adds an owner pubkey (hex) to the trusted set and persists it.
func PinOwner(dir, ownerPubHex string) error {
	return ProvisionOwner(dir, ownerPubHex, "", "")
}

// ProvisionOwner pins an owner and optionally stores the opaque registry record
// that owner created during pairing. The record is safe for an untrusted agent
// host to retain: only the owner can open or forge it.
func ProvisionOwner(dir, ownerID, registryBlob, registrationAuth string) error {
	cfg := &Config{}
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return err
	}
	if !cfg.IsOwnerPinned(ownerID) {
		cfg.PairedOwners = append(cfg.PairedOwners, ownerID)
	}
	if registryBlob != "" {
		if cfg.OwnerRegistry == nil {
			cfg.OwnerRegistry = make(map[string]string)
		}
		cfg.OwnerRegistry[ownerID] = registryBlob
	}
	if registrationAuth != "" {
		if cfg.OwnerRegistrationAuth == nil {
			cfg.OwnerRegistrationAuth = make(map[string]string)
		}
		cfg.OwnerRegistrationAuth[ownerID] = registrationAuth
	}
	return save(dir, cfg)
}

// RegistryForOwner reloads an owner's opaque discovery record so a running
// agent sees newly completed pairings without receiving any owner secret.
func RegistryForOwner(dir, ownerID string) string {
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return ""
	}
	var cfg Config
	if json.Unmarshal(data, &cfg) != nil || cfg.OwnerRegistry == nil {
		return ""
	}
	return cfg.OwnerRegistry[ownerID]
}

// RegistrationAuthForOwner reloads the public owner authorization used when
// this agent registers its owner|machine slot with the relay.
func RegistrationAuthForOwner(dir, ownerID string) string {
	data, err := os.ReadFile(configPath(dir))
	if err != nil {
		return ""
	}
	var cfg Config
	if json.Unmarshal(data, &cfg) != nil || cfg.OwnerRegistrationAuth == nil {
		return ""
	}
	return cfg.OwnerRegistrationAuth[ownerID]
}

// RegistrationCommitment is the value an owner signs during pairing. It is
// safe to reveal and does not permit recovery of the 256-bit random secret.
func (c *Config) RegistrationCommitment() (string, error) {
	secret, err := hex.DecodeString(c.RegistrationSecret)
	if err != nil || len(secret) != 32 {
		return "", fmt.Errorf("invalid agent registration secret")
	}
	h := sha256.Sum256(secret)
	return hex.EncodeToString(h[:]), nil
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
