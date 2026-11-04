package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/osprocess"
)

// RecordedConnectionFormat versions the per-root connection identity file.
const RecordedConnectionFormat = 1

// ErrInvalidConnectionRecord refuses an unknown or inconsistent connection identity file,
// which stays on disk untouched.
var ErrInvalidConnectionRecord = errors.New("adapter: invalid recorded connection identity")

const recordedConnectionsDir = "connections"

// ProcessIdentity names one process across PID reuse and reboot.
type ProcessIdentity struct {
	PID     int    `json:"pid"`
	Started int64  `json:"started"`
	BootID  string `json:"boot_id,omitempty"`
}

// CurrentProcessIdentity identifies pid as it runs now.
func CurrentProcessIdentity(pid int) (ProcessIdentity, error) {
	incarnation, ok := osprocess.CurrentIncarnation(pid)
	if !ok {
		return ProcessIdentity{}, fmt.Errorf("adapter: process %d has no readable start time", pid)
	}
	boot, _ := osprocess.BootID()
	return ProcessIdentity{PID: incarnation.PID, Started: incarnation.Started, BootID: boot}, nil
}

// Ended requires positive evidence that the recorded process is gone. A record from
// an earlier boot is gone even if the PID and start value happen to repeat.
func (p ProcessIdentity) Ended() bool {
	if p.BootID != "" {
		if boot, ok := osprocess.BootID(); ok && boot != p.BootID {
			return true
		}
	}
	return osprocess.Incarnation{PID: p.PID, Started: p.Started}.Ended()
}

// RecordedConnection is the durable identity of one mounted root and the process serving
// it. A mounted adapter records it before mounting and completes it once the kernel has
// assigned the connection, so a crash at any point leaves an identity to settle by.
type RecordedConnection struct {
	FormatVersion int             `json:"format_version"`
	AdapterID     AdapterID       `json:"adapter_id"`
	ViewID        string          `json:"view_id"`
	RootID        string          `json:"root_id"`
	MountPath     string          `json:"mount_path"`
	FSName        string          `json:"fs_name"`
	MountID       string          `json:"mount_id,omitempty"`
	ConnectionID  string          `json:"connection_id,omitempty"`
	DevMajorMinor string          `json:"dev_major_minor,omitempty"`
	OwnerUID      uint32          `json:"owner_uid"`
	Server        ProcessIdentity `json:"server"`
	RecordedAt    time.Time       `json:"recorded_at"`
}

// ConnectionDisposition is the outcome of settling one recorded connection.
type ConnectionDisposition string

const (
	// ConnectionAbsent means no mount with the recorded identity remains.
	ConnectionAbsent ConnectionDisposition = "absent"
	// ConnectionDetached means the mount with the recorded identity was removed.
	ConnectionDetached ConnectionDisposition = "detached"
	// ConnectionServerAlive means the recorded server still runs or its death is unproven.
	ConnectionServerAlive ConnectionDisposition = "server_alive"
	// ConnectionForeign means another mount occupies the recorded path.
	ConnectionForeign ConnectionDisposition = "foreign"
	// ConnectionUnresolved means the mount could not be removed.
	ConnectionUnresolved ConnectionDisposition = "unresolved"
)

// ConnectionSettlement reports what settling a recorded connection established.
type ConnectionSettlement struct {
	Disposition ConnectionDisposition
	// ConnectionAborted reports a kernel connection abort for a proven-dead server.
	ConnectionAborted bool
	Diagnostic        string
}

// Settled reports that no mount with this identity can still serve or accept writes.
func (s ConnectionSettlement) Settled() bool {
	return s.Disposition == ConnectionAbsent || s.Disposition == ConnectionDetached
}

// ConnectionRecovery settles connections recorded by an earlier engine process. Processes
// holding a settled mount see a dead filesystem and are never signaled.
type ConnectionRecovery interface {
	SettleRecordedConnection(ctx context.Context, rec RecordedConnection) (ConnectionSettlement, error)
}

// WriteRecordedConnection durably publishes one root's connection identity in its view's
// runtime directory.
func WriteRecordedConnection(runtimeViewDir string, rec RecordedConnection) error {
	if strings.TrimSpace(rec.ViewID) == "" || strings.TrimSpace(rec.RootID) == "" || strings.TrimSpace(rec.MountPath) == "" {
		return fmt.Errorf("%w: view, root and mount path are required", ErrInvalidConnectionRecord)
	}
	rec.FormatVersion = RecordedConnectionFormat
	rec.RecordedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(runtimeViewDir, recordedConnectionsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: recordFileName(rec.RootID)},
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// LoadRecordedConnections reads a view's connection identities. A view with none reads
// as empty; an unreadable file is refused rather than treated as absent.
func LoadRecordedConnections(runtimeViewDir string) ([]RecordedConnection, error) {
	dir := filepath.Join(runtimeViewDir, recordedConnectionsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RecordedConnection
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := loadRecordedConnection(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RootID < out[j].RootID })
	return out, nil
}

func loadRecordedConnection(path string) (RecordedConnection, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RecordedConnection{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec RecordedConnection
	if err := dec.Decode(&rec); err != nil {
		return RecordedConnection{}, fmt.Errorf("%w: %s: %w", ErrInvalidConnectionRecord, path, err)
	}
	if rec.FormatVersion != RecordedConnectionFormat {
		return RecordedConnection{}, fmt.Errorf("%w: %s: format %d", ErrInvalidConnectionRecord, path, rec.FormatVersion)
	}
	if rec.ViewID == "" || rec.RootID == "" || rec.MountPath == "" || filepath.Base(path) != recordFileName(rec.RootID) {
		return RecordedConnection{}, fmt.Errorf("%w: %s: inconsistent identity", ErrInvalidConnectionRecord, path)
	}
	return rec, nil
}

func recordFileName(rootID string) string {
	return strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(rootID) + ".json"
}
