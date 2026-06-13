// go/internal/client/store.go
package client

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// Identity is the client's owner identity (owner.json). New identities are
// prf-rooted: a 32-byte secret derives BOTH the X25519 transport key (owner_priv)
// and the Solana wallet (wallet_address). Legacy identities created before B1
// have only owner_priv (a directly-generated X25519 key) and no secret — they
// keep working for transport but have no wallet until re-keyed.
type Identity struct {
	SecretHex     string `json:"secret,omitempty"`         // 32-byte prf root (hex); absent on legacy identities
	OwnerPrivHex  string `json:"owner_priv"`               // X25519 transport private key (hex)
	OwnerPubHex   string `json:"owner_pub"`                // X25519 transport public key (hex) — the legacy owner_id
	WalletAddress string `json:"wallet_address,omitempty"` // base58 Solana address — the wallet owner_id
	DeviceID      string `json:"device_id,omitempty"`      // stable per-identity device id (binding.device)
	BindingJSON   string `json:"binding,omitempty"`        // cached wallet->x25519 binding (signed at re-key)
}

// Machine is a known agent (machines.json), pinned by host pubkey.
type Machine struct {
	Name       string `json:"name"`
	MachineID  string `json:"machine_id"`
	HostPubHex string `json:"host_pub"`
	SignalURL  string `json:"signal_url"`
}

func identityPath(dir string) string { return filepath.Join(dir, "owner.json") }
func machinesPath(dir string) string { return filepath.Join(dir, "machines.json") }

// IdentityExists reports whether an owner identity is already stored in dir, so the
// CLI can show a one-time intro the first time it creates one.
func IdentityExists(dir string) bool {
	_, err := os.Stat(identityPath(dir))
	return err == nil
}

// LoadOrCreateIdentity reads owner.json, creating a fresh owner keypair on first use.
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	id := &Identity{}
	if data, err := os.ReadFile(identityPath(dir)); err == nil {
		if err := json.Unmarshal(data, id); err != nil {
			return nil, err
		}
	}
	if id.OwnerPrivHex == "" {
		// Fresh identity: prf-rooted. One secret derives transport + wallet.
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := id.SetFromSecret(secret); err != nil {
			return nil, err
		}
		if err := SaveIdentity(dir, id); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(identityPath(dir), 0o600)
	return id, nil
}

// SetFromSecret roots the identity in a 32-byte prf secret, deriving both the
// X25519 transport key and the Solana wallet from it.
func (i *Identity) SetFromSecret(secret []byte) error {
	priv, pub, err := identity.DeriveOwnerKey(secret)
	if err != nil {
		return err
	}
	w, err := identity.DeriveWallet(secret)
	if err != nil {
		return err
	}
	i.SecretHex = hex.EncodeToString(secret)
	i.OwnerPrivHex = hex.EncodeToString(priv)
	i.OwnerPubHex = hex.EncodeToString(pub)
	i.WalletAddress = w.Address
	if i.DeviceID == "" {
		d := make([]byte, 8)
		if _, err := rand.Read(d); err != nil {
			return err
		}
		i.DeviceID = hex.EncodeToString(d)
	}
	sb, err := w.SignBinding(i.DeviceID, i.OwnerPubHex, time.Now().Unix())
	if err != nil {
		return err
	}
	rec, err := sb.JSON()
	if err != nil {
		return err
	}
	i.BindingJSON = rec
	return nil
}

// Rekey replaces the identity with a fresh prf-rooted one (new secret -> new
// owner_id + wallet). Used to migrate a legacy identity or rotate keys. Machines
// pinned to the old owner_id must be re-paired.
func Rekey(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	id := &Identity{}
	if err := id.SetFromSecret(secret); err != nil {
		return nil, err
	}
	if err := SaveIdentity(dir, id); err != nil {
		return nil, err
	}
	return id, nil
}

// SaveIdentity writes owner.json with 0600 perms (it holds the root secret).
func SaveIdentity(dir string, id *Identity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(identityPath(dir), data, 0o600); err != nil {
		return err
	}
	return os.Chmod(identityPath(dir), 0o600)
}

func (i *Identity) OwnerPriv() []byte { b, _ := hex.DecodeString(i.OwnerPrivHex); return b }
func (i *Identity) OwnerPub() []byte  { b, _ := hex.DecodeString(i.OwnerPubHex); return b }

// Secret returns the 32-byte prf root, or nil for a legacy identity.
func (i *Identity) Secret() []byte { b, _ := hex.DecodeString(i.SecretHex); return b }

// HasWallet reports whether this identity is prf-rooted (and thus has a wallet).
func (i *Identity) HasWallet() bool { return i.SecretHex != "" }

// Wallet derives the account-0 Solana wallet, or errors for a legacy identity.
func (i *Identity) Wallet() (*identity.Wallet, error) {
	if !i.HasWallet() {
		return nil, fmt.Errorf("this identity predates wallets (no secret root); re-key with `mir keygen --wallet` to create one")
	}
	return identity.DeriveWallet(i.Secret())
}

// AddMachine inserts or updates a known machine by name.
func AddMachine(dir string, m Machine) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	list, err := ListMachines(dir)
	if err != nil {
		// Refuse to mutate a store we couldn't read: the pinned host pubkeys
		// anchor the Noise KK trust decision, so silently overwriting them with
		// just the new entry would lose the user's pin set. Propagate instead.
		return err
	}
	updated := false
	for i := range list {
		if list[i].Name == m.Name {
			list[i] = m
			updated = true
			break
		}
	}
	if !updated {
		list = append(list, m)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically (temp file + rename) so a crash mid-write can't truncate
	// the real machines.json into the corrupt state this guards against.
	tmp, err := os.CreateTemp(dir, "machines-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, machinesPath(dir)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func ListMachines(dir string) ([]Machine, error) {
	data, err := os.ReadFile(machinesPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list []Machine
	err = json.Unmarshal(data, &list)
	return list, err
}

func GetMachine(dir, name string) (*Machine, error) {
	list, err := ListMachines(dir)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("unknown machine %q (add it with `mir add-machine`)", name)
}
