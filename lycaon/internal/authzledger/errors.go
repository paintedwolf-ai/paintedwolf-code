package authzledger

import "errors"

// ErrSealFailed indicates a granting authz decision could not be recorded (fail-closed gate).
var ErrSealFailed = errors.New("AUTHZ_SEAL_FAILED")
