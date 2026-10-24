// Package sourceeffect defines journaling for native file changes.
package sourceeffect

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Memory admits operation scratch and retained traversal state without granting access.
type Memory interface {
	Reserve(context.Context, int64) (func(), error)
	ReserveProgramState(context.Context, int64) (func(), error)
}

// Journal records intent, observed completion and source attribution for file effects.
type Journal interface {
	// RecoveryQuotaBytes bounds recovery representation charges per project.
	// Manifest content and retained tree allocation keep their own byte facts.
	RecoveryQuotaBytes() int64
	CanMoveAside(context.Context, fseffect.Location) (bool, error)
	InspectRemoval(context.Context, fseffect.Location, Memory) (RemovalSnapshot, error)
	PrepareEffect(context.Context, Plan) (Pending, error)
	PrepareEffects(context.Context, Memory, []Plan) (PendingEffects, error)
	SettleEffects(context.Context, []PendingEffects) error
	// RemoveEntry retains an entry for Undo before removing its original path.
	// It returns the operation identity.
	RemoveEntry(context.Context, Removal) (string, error)
}

// RemovalSnapshot names the exact tree measured before removal review.
type RemovalSnapshot struct {
	Identity          string `json:"identity"`
	TreeSHA           string `json:"tree_sha256"`
	Bytes             int64  `json:"bytes"`
	Files             int64  `json:"files"`
	RecoverySizeKnown bool   `json:"recovery_size_known"`
	RecoveryBytes     int64  `json:"recovery_bytes"`
	RecoveryAvailable bool   `json:"recovery_available"`
}

// RecoveryUnavailableError carries a failed recovery capability, without
// authorizing disposal. Only the caller's permanent-removal review can do that.
type RecoveryUnavailableError struct{ Cause error }

func (e *RecoveryUnavailableError) Error() string {
	if e.Cause == nil {
		return ""
	}
	return e.Cause.Error()
}

func (e *RecoveryUnavailableError) Unwrap() error { return e.Cause }

// ReviewedFile is a child instruction or policy file whose content was reviewed
// before a recursive tree removal.
type ReviewedFile struct {
	Rel    string `json:"rel"`
	SHA256 string `json:"sha256"`
}

// Removal is an entry the agent reviewed for removal from an attached root.
type Removal struct {
	Memory Memory `json:"-"`
	// Record carries the attribution and root-relative path; its Op is delete.
	Record sourceledger.RecordInput `json:"record"`
	Change sourcefeed.Change        `json:"change"`
	// RootPath is the attached root the path is relative to.
	RootPath string `json:"root_path"`
	// ReviewedSHA256 is the file content the review showed; a file that no
	// longer matches is refused. Empty for directories and links.
	ReviewedSHA256   string           `json:"reviewed_sha256,omitempty"`
	ReviewedFiles    []ReviewedFile   `json:"reviewed_files,omitempty"`
	ReviewedSnapshot *RemovalSnapshot `json:"reviewed_snapshot,omitempty"`
	// Recursive allows removing a non-empty directory tree as one operation.
	Recursive bool `json:"recursive,omitempty"`
	// Unrecoverable skips recovery capture after explicit user approval of a
	// permanent removal when same-filesystem recovery is unavailable.
	Unrecoverable bool `json:"unrecoverable,omitempty"`
}

// Pending records completion of a prepared file effect.
type Pending interface {
	ID() string
	Finish(context.Context, error) error
}

// Plan contains the postcondition and attribution used to recover an effect
// without repeating the filesystem mutation.
type Plan struct {
	Record sourceledger.RecordInput `json:"record"`
	Change sourcefeed.Change        `json:"change"`
	Target fseffect.Location        `json:"target"`
	From   fseffect.Location        `json:"from,omitempty"`
	// MetadataOnly effects cannot be inferred from content after interruption.
	MetadataOnly bool `json:"metadata_only,omitempty"`
}

// AppliedError reports a completion error after the file change took effect.
type AppliedError struct {
	OperationID string
	Cause       error
}

func (e *AppliedError) Error() string {
	return fmt.Sprintf("file change applied; completion reported an error (operation %s): %v", e.OperationID, e.Cause)
}

func (e *AppliedError) Unwrap() error { return e.Cause }

// Completion describes an actual publication attempt. An unattempted entry or
// PublicationNotCommitted cannot establish an effect by comparing destination bytes.
type Completion struct {
	Attempted bool
	Result    fseffect.Result
	Err       error
}

// PendingEffects holds individually identified intents and their completion facts.
// Record follows publication or abandonment, including cancellation. Settlement
// follows a successful Record before the caller returns. Close releases leases;
// durable applied markers remain recoverable if settlement fails.
type PendingEffects interface {
	IDs() []string
	Record(context.Context, []Completion) error
	Close()
}
