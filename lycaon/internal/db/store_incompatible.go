package db

import "fmt"

// RecoveryReason is the closed set GET /health reports when status is recovery.
type RecoveryReason string

const (
	RecoveryReasonIntegrityFailed RecoveryReason = "integrity_failed"
	RecoveryReasonSchemaMismatch  RecoveryReason = "schema_mismatch"
)

// StoreIncompatibleError describes a store this build cannot serve.
type StoreIncompatibleError struct {
	Reason             RecoveryReason
	StoreSchemaVersion int
	Detail             string
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
