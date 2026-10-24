package db

import "fmt"

// RecoveryReason is the closed set GET /health reports when status is recovery.
type RecoveryReason string

const (
	RecoveryReasonIntegrityFailed RecoveryReason = "integrity_failed"
	RecoveryReasonSchemaMismatch  RecoveryReason = "schema_mismatch"
	// RecoveryReasonInsufficientSpace reports a capacity refusal or exhausted storage.
	RecoveryReasonInsufficientSpace  RecoveryReason = "insufficient_space"
	RecoveryReasonStorageUnavailable RecoveryReason = "storage_unavailable"
)

// StoreIncompatibleError describes a store this build cannot serve.
type StoreIncompatibleError struct {
	Reason             RecoveryReason
	StoreSchemaVersion int
	Detail             string
	// Space is set when a preflight estimate is available.
	Space *InsufficientSpaceError
}

// InsufficientSpaceError is an upgrade the volume cannot finish.
type InsufficientSpaceError struct {
	NeededBytes    uint64
	AvailableBytes uint64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("recovery and database work need approximately %.1f GiB free; %.1f GiB is available",
		float64(e.NeededBytes)/(1<<30), float64(e.AvailableBytes)/(1<<30))
}

func (e *StoreIncompatibleError) Error() string {
	if e == nil {
		return ErrStoreIncompatible.Error()
	}
	if e.Detail == "" {
		return ErrStoreIncompatible.Error()
	}
	return fmt.Sprintf("%s: %s", ErrStoreIncompatible.Error(), e.Detail)
}

func (e *StoreIncompatibleError) Unwrap() error { return ErrStoreIncompatible }

func storeIncompatible(reason RecoveryReason, storeSchemaVersion int, detail string) error {
	return &StoreIncompatibleError{
		Reason:             reason,
		StoreSchemaVersion: storeSchemaVersion,
		Detail:             detail,
	}
}
