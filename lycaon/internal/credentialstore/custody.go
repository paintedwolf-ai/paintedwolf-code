package credentialstore

import (
	"errors"
	"path/filepath"
)

// CustodyMode names who holds a vault's identity.
type CustodyMode string

const (
	CustodyKeychain        CustodyMode = "keychain"
	CustodyIdentityService CustodyMode = "identity_service"
	CustodyAppPassword     CustodyMode = "app_password"
	CustodyDevelopment     CustodyMode = "development"
	// CustodyRefused: the identity service holds this user's identities but
	// refused this engine.
	CustodyRefused     CustodyMode = "refused"
	CustodyUnavailable CustodyMode = "unavailable"
)

// CustodyReport says which custodian holds one vault's identity.
type CustodyReport struct {
	Mode CustodyMode `json:"mode"`
	// ServiceVerified: the identity service verified this engine, so the
	// identity can move between the service and the app password.
	ServiceVerified bool   `json:"service_verified"`
	Detail          string `json:"detail,omitempty"`
}

// CustodyOutcome is the one line a custody move prints for the desktop app.
type CustodyOutcome struct {
	// Code is CustodyErrorCode of the move's error.
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

var (
	// ErrCustodianUnavailable: access to the identity custodian could not be established.
	ErrCustodianUnavailable = errors.New("the credential vault custodian is unavailable")
	// ErrIdentityMissing: the selected custodian has no key for this vault.
	ErrIdentityMissing = errors.New("the credential vault identity is missing")
	// ErrLauncherRefused: the identity service refused this engine.
	ErrLauncherRefused = errors.New("the vault identity service refused this engine")
	// ErrCustodyUnsupported: this platform keeps the identity with one custodian.
	ErrCustodyUnsupported = errors.New("this platform keeps the vault identity with one custodian")
	// ErrServiceUnverified: no identity service verified this engine.
	ErrServiceUnverified = errors.New("the vault identity service has not verified this engine")
	// ErrCustodyUnchanged: the identity is already where the move would put it.
	ErrCustodyUnchanged = errors.New("the vault identity is already held there")
	// ErrUnlockSecretInvalid: a new app password is too short or blank.
	ErrUnlockSecretInvalid = errors.New("the app password is invalid")
)

// DescribeCustody reports who holds the identity of the vault at vaultPath.
func DescribeCustody(vaultPath string) (CustodyReport, error) {
	if isolatedProcess() || !releaseBuild() {
		return CustodyReport{Mode: CustodyDevelopment}, nil
	}
	return describeCustody(filepath.Clean(vaultPath))
}

// MoveIdentityToService hands an app-password-wrapped identity to the
// identity service, using the configured unlock secret to unwrap it, and
// removes the wrapped copy once the service holds the same identity.
func MoveIdentityToService(vaultPath string) error {
	if isolatedProcess() || !releaseBuild() {
		return ErrCustodyUnsupported
	}
	return moveToService(filepath.Clean(vaultPath))
}

// MoveIdentityToAppPassword wraps the service-held identity with the
// configured unlock secret and removes it from the service once the wrapped
// copy opens with that password.
func MoveIdentityToAppPassword(vaultPath string) error {
	if isolatedProcess() || !releaseBuild() {
		return ErrCustodyUnsupported
	}
	return moveToAppPassword(filepath.Clean(vaultPath))
}

// CustodyErrorCode classifies a custody failure for the desktop app.
func CustodyErrorCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrVaultUnlockFailed):
		return "password_incorrect"
	case errors.Is(err, ErrVaultLocked):
		return "password_required"
	case errors.Is(err, ErrUnlockSecretInvalid):
		return "password_invalid"
	case errors.Is(err, ErrCustodyUnchanged):
		return "unchanged"
	case errors.Is(err, ErrCustodianUnavailable), errors.Is(err, ErrServiceUnverified), errors.Is(err, ErrLauncherRefused), errors.Is(err, ErrCustodyUnsupported):
		return "service_unavailable"
	default:
		return "failed"
	}
}
