package blueprint

import "errors"

var (
	ErrNotFound      = errors.New("plan not found")
	ErrInvalidStatus = errors.New("invalid plan status transition")
	// ErrPathTaken means a blueprint appeared at the minted path before the
	// commit; Create never replaces.
	ErrPathTaken = errors.New("blueprint path already exists")
	// ErrContentChanged means the destination no longer holds the bytes the
	// update was computed from. A governing document is reviewed whole, so
	// conflicting edits are refused rather than merged.
	ErrContentChanged = errors.New("blueprint content changed since it was read")
	// ErrRunActive means a non-terminal workflow run is bound to this blueprint
	// path, so deleting it would leave that run pointing at a missing file.
	ErrRunActive = errors.New("a workflow run is still using this blueprint")
)
