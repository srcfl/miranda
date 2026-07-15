package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

const maxRevocationResponseBytes = 4 << 20

type localRevocations struct {
	V           int                   `json:"v"`
	Revocations []identity.Revocation `json:"revocations"`
}

func revocationsPath(dir string) string { return filepath.Join(dir, "revocations.json") }

// ListRevocations loads and verifies the local owner-signed tombstones. A
// corrupt store fails closed: callers must not silently attach while the local
// deny-list cannot be trusted.
func ListRevocations(dir string) ([]identity.Revocation, error) {
	data, err := os.ReadFile(revocationsPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file localRevocations
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("revocations: decode local store: %w", err)
	}
	if file.V != 1 {
		return nil, fmt.Errorf("revocations: unsupported local store version %d", file.V)
	}
	for i := range file.Revocations {
		if err := identity.VerifyRevocation(&file.Revocations[i]); err != nil {
			return nil, fmt.Errorf("revocations: invalid local record: %w", err)
		}
	}
	return file.Revocations, nil
}

// RecordRevocation verifies and atomically stores a permanent tombstone.
func RecordRevocation(dir string, record identity.Revocation) error {
	if err := identity.VerifyRevocation(&record); err != nil {
		return err
	}
	records, err := ListRevocations(dir)
	if err != nil {
		return err
	}
	replaced := false
	for i := range records {
		if records[i].OwnerID == record.OwnerID && records[i].MachineID == record.MachineID {
			records[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].OwnerID == records[j].OwnerID {
			return records[i].MachineID < records[j].MachineID
		}
		return records[i].OwnerID < records[j].OwnerID
	})
	data, err := json.MarshalIndent(localRevocations{V: 1, Revocations: records}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "revocations-*.tmp")
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
	return os.Rename(tmpName, revocationsPath(dir))
}

// FilterRevoked removes machines covered by a verified tombstone for ownerID.
func FilterRevoked(machines []Machine, ownerID string, records []identity.Revocation) []Machine {
	revoked := make(map[string]bool, len(records))
	for _, record := range records {
		if record.OwnerID == ownerID {
			revoked[record.MachineID] = true
		}
	}
	out := make([]Machine, 0, len(machines))
	for _, machine := range machines {
		if !revoked[machine.MachineID] {
			out = append(out, machine)
		}
	}
	return out
}

func IsMachineRevoked(machineID, ownerID string, records []identity.Revocation) bool {
	for _, record := range records {
		if record.OwnerID == ownerID && record.MachineID == machineID {
			return true
		}
	}
	return false
}

// FetchRevocations obtains owner-signed records from a relay and independently
// verifies every signature. The relay may suppress records but cannot forge one.
func FetchRevocations(ctx context.Context, hc *http.Client, signalURL, ownerID string) ([]identity.Revocation, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	url := strings.TrimRight(signalURL, "/") + "/revocations?owner_id=" + neturl.QueryEscape(ownerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("revocations: relay returned %s", resp.Status)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxRevocationResponseBytes))
	var records []identity.Revocation
	if err := dec.Decode(&records); err != nil {
		return nil, fmt.Errorf("revocations: decode relay response: %w", err)
	}
	for i := range records {
		if records[i].OwnerID != ownerID {
			return nil, fmt.Errorf("revocations: relay returned a record for another owner")
		}
		if err := identity.VerifyRevocation(&records[i]); err != nil {
			return nil, fmt.Errorf("revocations: relay returned an invalid record: %w", err)
		}
	}
	return records, nil
}

// PostRevocation publishes a signed tombstone. The caller should persist it
// locally first, so a relay outage never re-enables the machine on this client.
func PostRevocation(ctx context.Context, hc *http.Client, signalURL string, record identity.Revocation) error {
	if err := identity.VerifyRevocation(&record); err != nil {
		return err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	url := strings.TrimRight(signalURL, "/") + "/revocations"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("revocations: relay returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
