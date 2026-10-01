// Package sourceeffect defines journaling for native file changes.
package sourceeffect

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Journal records intent and attribution before a file effect.
type Journal interface {
	PrepareEffect(context.Context, Plan) (Pending, error)
	// RemoveEntry keeps a complete recovery copy of an entry, then removes it,
	// so the removal can be undone. It returns the operation identity.
	RemoveEntry(context.Context, Removal) (string, error)
}

// Removal is an entry the agent reviewed for removal from an attached root.
type Removal struct {
	// Record carries the attribution and root-relative path; its Op is delete.
	Record sourceledger.RecordInput `json:"record"`
	Change sourcefeed.Change        `json:"change"`
	// RootPath is the attached root the path is relative to.
	RootPath string `json:"root_path"`
	// ReviewedSHA256 is the file content the review showed; a file that no
	// longer matches is refused. Empty for directories and links.
	ReviewedSHA256 string `json:"reviewed_sha256,omitempty"`
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
