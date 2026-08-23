package signal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/srcful/terminal-relay/go/internal/identity"
)

const maxRevocationBodyBytes = 16 << 10

type revocationFile struct {
	V           int                   `json:"v"`
	Revocations []identity.Revocation `json:"revocations"`
}

// LoadRevocations enables durable revocation storage and loads an existing
// file. Every entry is signature-verified before any state is accepted. A
// corrupt file fails startup instead of silently re-enabling revoked machines.
func (s *Server) LoadRevocations(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("revocations: empty file path")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("revocations: read: %w", err)
	}
	loaded := make(map[string]identity.Revocation)
	if err == nil {
		var file revocationFile
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("revocations: decode: %w", err)
		}
		if file.V != 1 {
			return fmt.Errorf("revocations: unsupported file version %d", file.V)
		}
		if s.maxRevocations > 0 && len(file.Revocations) > s.maxRevocations {
			return fmt.Errorf("revocations: file exceeds capacity")
		}
		for _, record := range file.Revocations {
			if err := identity.VerifyRevocation(&record); err != nil {
				return fmt.Errorf("revocations: invalid %s/%s: %w", shortID(record.OwnerID), shortID(record.MachineID), err)
			}
			loaded[key(record.OwnerID, record.MachineID)] = record
		}
	}
	durable := make(map[string]bool, len(loaded))
	for slot := range loaded {
		durable[slot] = true
	}
	s.mu.Lock()
	s.revoked = loaded
	s.revocationsFile = path
	s.mu.Unlock()
	s.persistMu.Lock()
	s.revDurable = durable
	s.persistMu.Unlock()
	return nil
}

func (s *Server) handleRevocations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getRevocations(w, r)
	case http.MethodPost:
		s.postRevocation(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getRevocations(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner_id"))
	if owner == "" {
		http.Error(w, "missing owner_id", http.StatusBadRequest)
		return
	}
	prefix := owner + "|"
	out := make([]identity.Revocation, 0)
	s.mu.Lock()
	for slot, record := range s.revoked {
		if strings.HasPrefix(slot, prefix) {
			out = append(out, record)
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].MachineID < out[j].MachineID })
	// Revocation freshness is security-relevant: a cached (pre-revocation) empty
	// list could let a client that relies on the relay fetch still attach to a
	// revoked machine. Forbid caching intermediaries from serving a stale list.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) postRevocation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRevocationBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var record identity.Revocation
	if err := dec.Decode(&record); err != nil {
		http.Error(w, "invalid revocation", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid revocation", http.StatusBadRequest)
		return
	}
	if err := identity.VerifyRevocation(&record); err != nil {
		s.logf("event=revocation_reject reason=signature ip=%s", s.remoteIP(r))
		http.Error(w, "invalid revocation signature", http.StatusUnauthorized)
		return
	}

	slot := key(record.OwnerID, record.MachineID)
	s.mu.Lock()
	existing, exists := s.revoked[slot]
	if exists && existing == record {
		durable := s.slotDurable(slot)
		s.mu.Unlock()
		if s.revocationsFile != "" && !durable {
			http.Error(w, "could not persist revocation", http.StatusInternalServerError)
			return
		}
		s.logf("event=machine_revoked owner=%s machine=%s duplicate=true ip=%s", shortID(record.OwnerID), shortID(record.MachineID), s.remoteIP(r))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !exists && s.maxRevocations > 0 && len(s.revoked) >= s.maxRevocations {
		s.mu.Unlock()
		http.Error(w, capacityReason, http.StatusServiceUnavailable)
		return
	}
	s.revoked[slot] = record
	s.revVersion++
	version := s.revVersion
	snapshot := s.snapshotRevocationsLocked()
	ac := s.agents[slot]
	delete(s.agents, slot)
	s.mu.Unlock()
	// Enforcement (tearing down the live agent) has already happened under s.mu;
	// durability is best-effort and now runs off the broker lock so a slow fsync
	// cannot stall signaling.
	if ac != nil {
		ac.teardown()
	}
	if err := s.persistRevocations(version, snapshot); err != nil {
		s.rollbackRevocation(slot, existing, exists)
		s.logf("event=revocation_persist_error owner=%s machine=%s", shortID(record.OwnerID), shortID(record.MachineID))
		http.Error(w, "could not persist revocation", http.StatusInternalServerError)
		return
	}
	s.logf("event=machine_revoked owner=%s machine=%s duplicate=%t ip=%s", shortID(record.OwnerID), shortID(record.MachineID), exists, s.remoteIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// snapshotRevocationsLocked returns a stable, sorted copy of the tombstone set.
// s.mu must be held. The copy is what the (lock-free) persister writes, so a slow
// fsync never touches s.revoked or blocks the broker lock.
func (s *Server) snapshotRevocationsLocked() []identity.Revocation {
	list := make([]identity.Revocation, 0, len(s.revoked))
	for _, record := range s.revoked {
		list = append(list, record)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].OwnerID == list[j].OwnerID {
			return list[i].MachineID < list[j].MachineID
		}
		return list[i].OwnerID < list[j].OwnerID
	})
	return list
}

// persistRevocations writes a versioned snapshot to disk under persistMu (NOT the
// broker lock). Writers are serialized here, and a snapshot whose version is
// already <= revPersisted is skipped: because every version bump happens under
// s.mu together with the map write, a higher-versioned snapshot is always a
// superset, so dropping the stale write still leaves a superset on disk.
func (s *Server) persistRevocations(version uint64, list []identity.Revocation) error {
	if s.revocationsFile == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if version <= s.revPersisted {
		return nil // a newer (superset) snapshot already reached disk
	}
	if err := s.writeRevocationsFile(list); err != nil {
		return err
	}
	s.revPersisted = version
	if s.revDurable == nil {
		s.revDurable = map[string]bool{}
	}
	for _, rec := range list {
		s.revDurable[key(rec.OwnerID, rec.MachineID)] = true
	}
	return nil
}

func (s *Server) slotDurable(slot string) bool {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return s.revDurable[slot]
}

func (s *Server) rollbackRevocation(slot string, existing identity.Revocation, existed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existed {
		s.revoked[slot] = existing
		return
	}
	delete(s.revoked, slot)
}

// writeRevocationsFile atomically writes the given snapshot. It reads no shared
// state, so it holds neither s.mu nor (beyond the caller) persistMu longer than
// the disk write. Callers pass a snapshot taken under s.mu.
func (s *Server) writeRevocationsFile(list []identity.Revocation) error {
	data, err := json.MarshalIndent(revocationFile{V: 1, Revocations: list}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.revocationsFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".revocations-*.tmp")
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.revocationsFile)
}
