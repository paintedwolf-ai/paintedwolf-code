package extensionstate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// revisionDomain separates authoring state from compiled catalog revisions.
const revisionDomain = "painted-wolf/extension-state-revision/1"

// stateFile is one on-disk state document snapshot.
type stateFile struct {
	Path    string
	Bytes   []byte
	Missing bool
}

func readStateFile(path string) (stateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stateFile{Path: path, Missing: true}, nil
		}
		return stateFile{}, err
	}
	return stateFile{Path: path, Bytes: data}, nil
}

// stateSnapshot contains every state file visible to one mutation.
type stateSnapshot struct {
	DeviceDesired  stateFile
	DeviceLock     stateFile
	ProjectDesired stateFile
	HasProject     bool
	Revision       string
}

// readStateSnapshot requires callers to lock commit decisions.
func readStateSnapshot(projectDir string) (stateSnapshot, error) {
	deviceDesiredPath, err := extpacks.DeviceDesiredPath()
	if err != nil {
		return stateSnapshot{}, err
	}
	deviceLockPath, err := extpacks.DeviceLockPath()
	if err != nil {
		return stateSnapshot{}, err
	}
	snap := stateSnapshot{}
	if snap.DeviceDesired, err = readStateFile(deviceDesiredPath); err != nil {
		return stateSnapshot{}, err
	}
	if snap.DeviceLock, err = readStateFile(deviceLockPath); err != nil {
		return stateSnapshot{}, err
	}
	if projectDir != "" {
		snap.HasProject = true
		if snap.ProjectDesired, err = readStateFile(extpacks.ProjectDesiredPath(projectDir)); err != nil {
			return stateSnapshot{}, err
		}
	}
	snap.Revision = revisionOf(snap)
	return snap, nil
}

// takeStateSnapshot reads under the intent locks for a consistent view.
func takeStateSnapshot(projectDir string) (stateSnapshot, error) {
	release, err := extpacks.AcquireIntentLocks([]string{projectDir})
	if err != nil {
		return stateSnapshot{}, err
	}
	defer release()
	return readStateSnapshot(projectDir)
}

func snapshotAfterCommit(snap stateSnapshot, scope, desiredPath, lockPath string, desired, lock []byte) stateSnapshot {
	committedDesired := stateFile{Path: desiredPath, Bytes: desired}
	if scope == "project" {
		snap.ProjectDesired = committedDesired
	} else {
		snap.DeviceDesired = committedDesired
		snap.DeviceLock = stateFile{Path: lockPath, Bytes: lock}
	}
	snap.Revision = revisionOf(snap)
	return snap
}

// revisionOf canonicalizes valid desired state and preserves invalid bytes.
func revisionOf(snap stateSnapshot) string {
	h := sha256.New()
	field := func(label string, body []byte) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f", label, len(body))
		_, _ = h.Write(body)
	}
	field("domain", []byte(revisionDomain))
	field("device.desired", canonicalDesiredBytes(snap.DeviceDesired))
	field("device.lock", canonicalLockBytes(snap.DeviceLock))
	if snap.HasProject {
		field("project.desired", canonicalSuggestionBytes(snap.ProjectDesired))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalDesiredBytes(file stateFile) []byte {
	if file.Missing {
		return []byte("missing")
	}
	desired, err := extpacks.ParseDesired(file.Path, file.Bytes)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	data, err := extpacks.EncodeDesired(desired)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	return data
}

func canonicalSuggestionBytes(file stateFile) []byte {
	if file.Missing {
		return []byte("missing")
	}
	sug, err := extpacks.ParseSuggestion(file.Path, file.Bytes)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	data, err := extpacks.EncodeSuggestion(sug)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	return data
}

func canonicalLockBytes(file stateFile) []byte {
	if file.Missing {
		return []byte("missing")
	}
	lock, err := extpacks.ParseLock(file.Path, file.Bytes)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	data, err := extpacks.EncodeLock(lock)
	if err != nil {
		return append([]byte("unparsed\x1f"), file.Bytes...)
	}
	return data
}
