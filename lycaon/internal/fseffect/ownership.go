package fseffect

import (
	"errors"
	"os"
	"time"
)

// OwnershipRequest addresses one descriptor-relative optional metadata effect.
// A negative UID or GID retains that component, as in the platform primitive.
type OwnershipRequest struct {
	Location
	UID int
	GID int
}

// MetadataCapability reports an observed capability result, never a platform guess.
type MetadataCapability uint8

const (
	MetadataUndetermined MetadataCapability = iota
	MetadataAvailable
	MetadataUnsupported
)

// MetadataResult never presents an unsupported or failed effect as applied.
// It does not promise metadata rollback; the calling domain retains its journal.
type MetadataResult struct {
	Applied    bool
	Capability MetadataCapability
}

// ChangeOwnership uses the existing platform metadata door. Unsupported hosts
// return ErrUnsupported without opening or changing a target.
func ChangeOwnership(req OwnershipRequest) (MetadataResult, error) {
	err := platformChown(req.Location, req.UID, req.GID)
	return ownershipOutcome(err), err
}

func ownershipOutcome(err error) MetadataResult {
	if errors.Is(err, ErrUnsupported) {
		return MetadataResult{Capability: MetadataUnsupported}
	}
	if err != nil {
		return MetadataResult{Capability: MetadataUndetermined}
	}
	return MetadataResult{Applied: true, Capability: MetadataAvailable}
}

// TimesRequest sets a target's access and modification times. A nil time is
// left unchanged. The target is reached without symlink traversal and a link
// itself is never retimed through its target.
type TimesRequest struct {
	Location
	Access, Modify *time.Time
}

// ChangeTimes uses the platform metadata door. Unsupported hosts return
// ErrUnsupported without opening or changing a target.
func ChangeTimes(req TimesRequest) (MetadataResult, error) {
	err := platformTimes(req.Location, req.Access, req.Modify)
	return ownershipOutcome(err), err
}

// AccessTime reports a file's last access time where the platform records it
// in the stat result; ok is false otherwise.
func AccessTime(info os.FileInfo) (time.Time, bool) { return platformAccessTime(info) }
