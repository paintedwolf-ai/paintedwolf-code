package hitl

import "errors"

var (
	// ErrCheckpointNotFound indicates no matching checkpoint exists.
	ErrCheckpointNotFound = errors.New("checkpoint not found")
	// ErrCheckpointNotPending indicates the checkpoint is already resolved.
	ErrCheckpointNotPending = errors.New("checkpoint not pending")
	// ErrCheckpointKindMismatch indicates a different stored kind.
	ErrCheckpointKindMismatch = errors.New("checkpoint kind mismatch")
	// ErrApprovalOptionNotFound indicates an unknown pending option.
	ErrApprovalOptionNotFound = errors.New("approval option not found")
	// ErrApprovalOptionUnavailable indicates a present but unselectable option.
	ErrApprovalOptionUnavailable = errors.New("approval option is not available")
	// ErrApprovalOptionRequired requires an explicit grant option.
	ErrApprovalOptionRequired = errors.New("approval option required")
	// ErrApprovalResolverInvalid rejects a policy resolution without a complete policy identity.
	ErrApprovalResolverInvalid = errors.New("approval resolver is invalid")
	// ErrContentApplyHunkNotFound indicates an unknown planned hunk.
	ErrContentApplyHunkNotFound = errors.New("content_apply hunk not found")
	// ErrContentApplySelectionInvalid indicates a malformed decision/hunk pairing.
	ErrContentApplySelectionInvalid = errors.New("content_apply hunk selection invalid")
)

// ErrContentApplyExpired reports an unanswered edit review.
const ErrContentApplyExpired = "edit review expired — no response within the review window; the edit was not applied. This is a timeout, not a denial: retry the call if you still need it."

// ErrContentApplyUnresolved reports an edit review without a decision.
const ErrContentApplyUnresolved = "edit review ended without a decision — the edit was not applied"
