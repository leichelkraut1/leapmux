package codexobserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const maxControlRecords = 2048

// The journal contains identifiers and payload digests, never input text or
// native frames. An attempted record is durable BEFORE a mutation is written;
// a process crash, disconnect, restart or failed final write cannot cause a
// replay. Terminal records expire only after their binding lease cannot pass
// prewrite checks; attempted/uncertain records are never automatically pruned.
type controlRecord struct {
	Version      int    `json:"version"`
	Fingerprint  string `json:"fingerprint"`
	Status       string `json:"status"`
	LeaseHash    string `json:"leaseHash"`
	LeaseExpires int64  `json:"leaseExpires"`
}

type controlJournal struct {
	directory       string
	verify          func(string) error
	ensureDirectory func(string) error
	syncDirectory   func(string) error
}

func (j controlJournal) prepare(now time.Time, live map[string]bool) error {
	if j.ensureDirectory(j.directory) != nil {
		return agent.ErrNativeControlUnavailable
	}
	marker := filepath.Join(j.directory, ".codex-control-ledger")
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, writeErr := f.WriteString("codex-owner-control-v1\n")
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil || j.syncDirectory(j.directory) != nil {
			return agent.ErrNativeControlUnavailable
		}
	} else if !errors.Is(err, os.ErrExist) {
		return agent.ErrNativeControlUnavailable
	}
	if j.verify(marker) != nil {
		return agent.ErrNativeControlUnavailable
	}
	info, err := os.Lstat(marker)
	if err != nil || info.Size() > 64 {
		return agent.ErrNativeControlUnavailable
	}
	value, err := os.ReadFile(marker)
	if err != nil || string(value) != "codex-owner-control-v1\n" {
		return agent.ErrNativeControlUnavailable
	}
	return j.prune(now, live)
}

func controlBindingHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (j controlJournal) read(path string) (controlRecord, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || j.verify(path) != nil {
		return controlRecord{}, agent.ErrNativeControlUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return controlRecord{}, agent.ErrNativeControlUnavailable
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return controlRecord{}, agent.ErrNativeControlUnavailable
	}
	var record controlRecord
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || len(record.Fingerprint) != 64 || len(record.LeaseHash) != 64 || record.LeaseExpires <= 0 {
		return controlRecord{}, agent.ErrNativeControlUnavailable
	}
	switch record.Status {
	case "attempted", "uncertain", "accepted", "stale", "unavailable":
		return record, nil
	default:
		return controlRecord{}, agent.ErrNativeControlUnavailable
	}
}

func (j controlJournal) prune(now time.Time, live map[string]bool) error {
	entries, err := os.ReadDir(j.directory)
	if err != nil || len(entries) > maxControlRecords*2+16 {
		return agent.ErrNativeControlUnavailable
	}
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "mutation-") || !strings.HasSuffix(name, ".json") || len(name) != len("mutation-")+64+len(".json") {
			continue
		}
		path := filepath.Join(j.directory, name)
		record, err := j.read(path)
		if err != nil {
			return err
		}
		if record.Status == "attempted" || record.Status == "uncertain" || live[record.LeaseHash] || record.LeaseExpires > now.UnixNano() {
			continue
		}
		if os.Remove(path) != nil {
			return agent.ErrNativeControlUnavailable
		}
		changed = true
	}
	if changed && j.syncDirectory(j.directory) != nil {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func (j controlJournal) path(id string) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(j.directory, "mutation-"+hex.EncodeToString(digest[:])+".json")
}

func (j controlJournal) existing(id, fingerprint string) (*controlRecord, error) {
	path := j.path(id)
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, agent.ErrNativeControlUnavailable
	}
	record, err := j.read(path)
	if err != nil {
		return nil, err
	}
	if record.Fingerprint != fingerprint {
		return nil, agent.ErrNativeControlInvalid
	}
	return &record, nil
}

func (j controlJournal) claim(id, fingerprint, binding string, expires time.Time) error {
	entries, err := os.ReadDir(j.directory)
	if err != nil || len(entries) > maxControlRecords {
		return agent.ErrNativeControlUnavailable
	}
	f, err := os.OpenFile(j.path(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return agent.ErrNativeControlUncertain
	}
	raw, _ := json.Marshal(controlRecord{Version: 1, Fingerprint: fingerprint, Status: "attempted", LeaseHash: controlBindingHash(binding), LeaseExpires: expires.UnixNano()})
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || j.verify(j.path(id)) != nil || j.syncDirectory(j.directory) != nil {
		return agent.ErrNativeControlUncertain
	}
	return nil
}

func (j controlJournal) finish(id, fingerprint, binding string, expires time.Time, result error) error {
	status := "accepted"
	switch {
	case errors.Is(result, agent.ErrNativeControlStale):
		status = "stale"
	case errors.Is(result, agent.ErrNativeControlUnavailable):
		status = "unavailable"
	case result != nil:
		status = "uncertain"
	}
	raw, _ := json.Marshal(controlRecord{Version: 1, Fingerprint: fingerprint, Status: status, LeaseHash: controlBindingHash(binding), LeaseExpires: expires.UnixNano()})
	f, err := os.CreateTemp(j.directory, ".codex-control-result-")
	if err != nil {
		return agent.ErrNativeControlUncertain
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || j.verify(name) != nil || j.verify(j.path(id)) != nil || os.Rename(name, j.path(id)) != nil || j.syncDirectory(j.directory) != nil {
		return agent.ErrNativeControlUncertain
	}
	return result
}

func controlReplay(record controlRecord) error {
	switch record.Status {
	case "accepted":
		return nil
	case "stale":
		return agent.ErrNativeControlStale
	case "unavailable":
		return agent.ErrNativeControlUnavailable
	default:
		return agent.ErrNativeControlUncertain
	}
}
