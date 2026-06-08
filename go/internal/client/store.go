// go/internal/client/store.go
package client

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/srcful/terminal-relay/go/internal/noise"
)

// Identity is the client's SSH-style owner keypair (owner.json).
type Identity struct {
	OwnerPrivHex string `json:"owner_priv"`
	OwnerPubHex  string `json:"owner_pub"`
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
		priv, pub, err := noise.GenerateStatic()
		if err != nil {
			return nil, err
		}
		id.OwnerPrivHex = hex.EncodeToString(priv)
		id.OwnerPubHex = hex.EncodeToString(pub)
		data, _ := json.MarshalIndent(id, "", "  ")
		if err := os.WriteFile(identityPath(dir), data, 0o600); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(identityPath(dir), 0o600)
	return id, nil
}

func (i *Identity) OwnerPriv() []byte { b, _ := hex.DecodeString(i.OwnerPrivHex); return b }
func (i *Identity) OwnerPub() []byte  { b, _ := hex.DecodeString(i.OwnerPubHex); return b }

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
