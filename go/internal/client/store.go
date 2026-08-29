// go/internal/client/store.go
package client

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

// Identity is trusted-client state. Modern identities keep their root in the OS
// keychain and persist only SecretRef plus public metadata in owner.json. Both
// private keys are re-derived in memory. SecretHex and OwnerPrivHex are read-only
// migration fields and are scrubbed on the first successful secure save.
type Identity struct {
	SecretHex    string `json:"secret,omitempty"`     // legacy plaintext root; migration only
	SecretRef    string `json:"secret_ref,omitempty"` // opaque OS-keychain account
	OwnerPrivHex string `json:"owner_priv,omitempty"` // legacy only; rooted identities re-derive in memory
	OwnerPubHex  string `json:"owner_pub"`            // X25519 transport public key
	OwnerID      string `json:"owner_id,omitempty"`   // neutral Ed25519 Miranda identity
	DeviceID     string `json:"device_id,omitempty"`  // stable per-identity device id (binding.device)
	BindingJSON  string `json:"binding,omitempty"`    // cached signer->x25519 binding

	secret            []byte `json:"-"`
	obsoleteSecretRef string `json:"-"`
}

// Machine is a known agent (machines.json), pinned by host pubkey.
type Machine struct {
	Name       string `json:"name"`
	MachineID  string `json:"machine_id"`
	HostPubHex string `json:"host_pub"`
	SignalURL  string `json:"signal_url"`
	// NameTS is when Name was last set (unix seconds; 0 = unknown/legacy).
	// Registry merge is last-writer-wins on it, so a rename made on one device
	// beats every stale name without ever letting a stale one bounce back.
	NameTS int64 `json:"name_ts,omitempty"`
}

type IdentityStorageInfo struct {
	Exists          bool
	OwnerID         string
	Backend         string
	LegacyPlaintext bool
}

func identityPath(dir string) string { return filepath.Join(dir, "owner.json") }
func machinesPath(dir string) string { return filepath.Join(dir, "machines.json") }

// IdentityExists reports whether an owner identity is already stored in dir, so the
// CLI can show a one-time intro the first time it creates one.
func IdentityExists(dir string) bool {
	_, err := os.Stat(identityPath(dir))
	return err == nil
}

// InspectIdentityStorage validates owner.json and its keychain entry without
// creating, migrating, chmodding, or otherwise mutating state.
func InspectIdentityStorage(dir string) (IdentityStorageInfo, error) {
	data, err := os.ReadFile(identityPath(dir))
	if os.IsNotExist(err) {
		return IdentityStorageInfo{}, nil
	}
	if err != nil {
		return IdentityStorageInfo{}, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return IdentityStorageInfo{}, fmt.Errorf("owner identity: invalid metadata: %w", err)
	}
	info := IdentityStorageInfo{Exists: true, OwnerID: id.OwnerID, LegacyPlaintext: id.SecretHex != "" || id.OwnerPrivHex != ""}
	if id.SecretRef == "" {
		if info.LegacyPlaintext {
			return info, nil
		}
		return info, fmt.Errorf("owner identity: no keychain reference or legacy identity material")
	}
	store, err := platformSecretStore()
	if err != nil {
		return info, err
	}
	encoded, err := store.Get(id.SecretRef)
	if err != nil {
		return info, err
	}
	defer zeroBytes(encoded)
	secret, err := decodeSecretHex(encoded)
	if err != nil {
		return info, err
	}
	defer zeroBytes(secret)
	_, pub, err := identity.DeriveOwnerKey(secret)
	if err != nil {
		return info, err
	}
	signer, err := identity.DeriveSigner(secret)
	if err != nil {
		return info, err
	}
	if id.OwnerID != "" && id.OwnerID != signer.Address {
		return info, fmt.Errorf("owner identity: public id does not match keychain root")
	}
	if id.OwnerPubHex != "" && id.OwnerPubHex != hex.EncodeToString(pub) {
		return info, fmt.Errorf("owner identity: transport public key does not match keychain root")
	}
	info.OwnerID = signer.Address
	info.Backend = store.Name()
	return info, nil
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
	if id.SecretRef != "" {
		store, err := platformSecretStore()
		if err != nil {
			return nil, err
		}
		encoded, err := store.Get(id.SecretRef)
		if err != nil {
			return nil, err
		}
		defer zeroBytes(encoded)
		secret, err := decodeSecretHex(encoded)
		if err != nil {
			return nil, err
		}
		defer zeroBytes(secret)
		if err := id.setFromSecret(secret, true); err != nil {
			return nil, err
		}
	} else if id.SecretHex != "" {
		secret, err := hex.DecodeString(id.SecretHex)
		if err != nil || len(secret) != 32 {
			return nil, fmt.Errorf("owner identity: secret must be 32 bytes")
		}
		defer zeroBytes(secret)
		if err := id.setFromSecret(secret, false); err != nil {
			return nil, err
		}
		// One-way migration: do not remove plaintext until the keychain write and
		// atomic owner.json replacement have both succeeded.
		if err := SaveIdentity(dir, id); err != nil {
			return nil, err
		}
	}
	if id.OwnerPrivHex == "" && len(id.secret) == 0 {
		// Fresh identity: one root derives independent transport and signing keys.
		secret := make([]byte, 32)
		defer zeroBytes(secret)
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

// SetFromSecret derives independent X25519 transport and Ed25519 signing keys.
func (i *Identity) SetFromSecret(secret []byte) error {
	return i.setFromSecret(secret, false)
}

func (i *Identity) setFromSecret(secret []byte, preserveRef bool) error {
	if len(secret) != 32 {
		return fmt.Errorf("owner identity: secret must be 32 bytes")
	}
	if !preserveRef && i.SecretRef != "" {
		i.obsoleteSecretRef = i.SecretRef
		i.SecretRef = ""
	}
	_, pub, err := identity.DeriveOwnerKey(secret)
	if err != nil {
		return err
	}
	w, err := identity.DeriveSigner(secret)
	if err != nil {
		return err
	}
	i.secret = append(i.secret[:0], secret...)
	i.SecretHex = ""
	i.OwnerPrivHex = "" // derived on demand; never persist this redundant private key
	i.OwnerPubHex = hex.EncodeToString(pub)
	i.OwnerID = w.Address
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

// Rekey replaces the identity with a fresh root. Machines
// pinned to the old owner_id must be re-paired.
func Rekey(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	defer zeroBytes(secret)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	id := &Identity{}
	if data, readErr := os.ReadFile(identityPath(dir)); readErr == nil {
		var old Identity
		if err := json.Unmarshal(data, &old); err != nil {
			return nil, fmt.Errorf("owner identity: cannot rotate corrupt metadata: %w", err)
		}
		id.obsoleteSecretRef = old.SecretRef
	} else if !os.IsNotExist(readErr) {
		return nil, readErr
	}
	if err := id.SetFromSecret(secret); err != nil {
		return nil, err
	}
	if err := SaveIdentity(dir, id); err != nil {
		return nil, err
	}
	return id, nil
}

// SaveIdentity stores the root in the OS keychain, then atomically writes only
// public metadata plus an opaque reference to owner.json.
func SaveIdentity(dir string, id *Identity) error {
	secret := id.Secret()
	if len(secret) != 32 {
		return fmt.Errorf("owner identity: no 32-byte root is loaded")
	}
	defer zeroBytes(secret)
	store, err := platformSecretStore()
	if err != nil {
		return err
	}
	targetRef := id.SecretRef
	createdRef := false
	if targetRef == "" {
		refBytes := make([]byte, 16)
		if _, err := rand.Read(refBytes); err != nil {
			return err
		}
		targetRef = "owner-" + hex.EncodeToString(refBytes)
		createdRef = true
	}
	encodedSecret := encodeSecretHex(secret)
	defer zeroBytes(encodedSecret)
	if err := store.Put(targetRef, encodedSecret); err != nil {
		return err
	}
	persisted := *id
	persisted.SecretRef = targetRef
	persisted.SecretHex = ""
	persisted.OwnerPrivHex = ""
	persisted.secret = nil
	persisted.obsoleteSecretRef = ""
	data, err := json.MarshalIndent(&persisted, "", "  ")
	if err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	tmp, err := os.CreateTemp(dir, "owner-*.json.tmp")
	if err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := tmp.Close(); err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	if err := os.Rename(tmpName, identityPath(dir)); err != nil {
		if createdRef {
			_ = store.Delete(targetRef)
		}
		return err
	}
	oldRef := id.obsoleteSecretRef
	id.SecretRef = targetRef
	id.SecretHex = ""
	id.OwnerPrivHex = ""
	id.obsoleteSecretRef = ""
	if oldRef != "" && oldRef != targetRef {
		if err := store.Delete(oldRef); err != nil {
			return fmt.Errorf("identity saved, but retired keychain entry cleanup failed: %w", err)
		}
	}
	return nil
}

func (i *Identity) OwnerPriv() []byte {
	if secret := i.Secret(); len(secret) == 32 {
		defer zeroBytes(secret)
		priv, _, err := identity.DeriveOwnerKey(secret)
		if err == nil {
			return priv
		}
	}
	b, _ := hex.DecodeString(i.OwnerPrivHex)
	return b
}
func (i *Identity) OwnerPub() []byte { b, _ := hex.DecodeString(i.OwnerPubHex); return b }

// Secret returns the 32-byte prf root, or nil for a legacy identity.
func (i *Identity) Secret() []byte {
	if len(i.secret) > 0 {
		return append([]byte(nil), i.secret...)
	}
	b, _ := hex.DecodeString(i.SecretHex) // legacy migration before first load
	return b
}

// HasRootedIdentity reports whether the modern Miranda identity root exists.
func (i *Identity) HasRootedIdentity() bool { return len(i.Secret()) == 32 }

// CheckSecretStorage verifies that the loaded root matches its OS-keychain
// entry. It is read-only and intended for `mir doctor`.
func (i *Identity) CheckSecretStorage() (string, error) {
	if i.SecretRef == "" || len(i.Secret()) != 32 {
		return "", fmt.Errorf("owner identity is not backed by a keychain reference")
	}
	store, err := platformSecretStore()
	if err != nil {
		return "", err
	}
	encoded, err := store.Get(i.SecretRef)
	if err != nil {
		return "", err
	}
	defer zeroBytes(encoded)
	stored, err := decodeSecretHex(encoded)
	if err != nil {
		return "", err
	}
	defer zeroBytes(stored)
	loaded := i.Secret()
	defer zeroBytes(loaded)
	if subtle.ConstantTimeCompare(stored, loaded) != 1 {
		return "", fmt.Errorf("keychain owner secret does not match the loaded identity")
	}
	return store.Name(), nil
}

// Signer re-derives the neutral Ed25519 owner signer in memory.
func (i *Identity) Signer() (*identity.Signer, error) {
	if !i.HasRootedIdentity() {
		return nil, fmt.Errorf("this identity predates Miranda identity v2; rotate with `mir identity rotate --yes` and re-pair")
	}
	secret := i.Secret()
	defer zeroBytes(secret)
	return identity.DeriveSigner(secret)
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
	return writeMachines(dir, list)
}

// writeMachines writes machines.json atomically (temp file + rename) so a crash
// mid-write can't truncate the pin store.
func writeMachines(dir string, list []Machine) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
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

// RenameLocalMachine sets a machine's display name (keyed by machine_id, the
// stable identifier — names are exactly what rename changes) with the rename
// timestamp the registry record was sealed at. A machine known only from the
// registry (not yet in machines.json) is added, so the rename survives the
// registry entry going away when the machine powers off.
func RenameLocalMachine(dir string, m Machine, name string, ts int64) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	list, err := ListMachines(dir)
	if err != nil {
		return err // never overwrite a pin store we couldn't read
	}
	found := false
	for i := range list {
		if list[i].MachineID == m.MachineID {
			list[i].Name = name
			list[i].NameTS = ts
			found = true
			break
		}
	}
	if !found {
		m.Name = name
		m.NameTS = ts
		list = append(list, m)
	}
	return writeMachines(dir, list)
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
	return nil, fmt.Errorf("unknown machine %q (pair it with `mir pair`)", name)
}
