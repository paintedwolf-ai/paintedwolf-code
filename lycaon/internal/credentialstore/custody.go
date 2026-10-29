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
	CustodyDevelopment     CustodyMode = "development"
	// CustodyRefused: the identity service holds this user's identities but
	// refused this engine.
	CustodyRefused     CustodyMode = "refused"
	CustodyUnavailable CustodyMode = "unavailable"
)

// CustodyReport says which custodian holds one vault's identity.
type CustodyReport struct {
	Mode CustodyMode `json:"mode"`
	// ServiceVerified: the identity service authenticated this engine.
	ServiceVerified bool   `json:"service_verified"`
	Detail          string `json:"detail,omitempty"`
}

var (
	// ErrCustodianUnavailable: access to the identity custodian could not be established.
	ErrCustodianUnavailable = errors.New("the credential vault custodian is unavailable")
	// ErrCustodianAbsent: the custodian that held this vault's key is not
	// installed, not running, or answered that it cannot serve; waiting does
	// not bring it back; restore the service before opening or resetting the vault.
	ErrCustodianAbsent = errors.New("the credential vault custodian is absent")
	// ErrIdentityMissing: the selected custodian has no key for this vault.
	ErrIdentityMissing = errors.New("the credential vault identity is missing")
	// ErrLauncherRefused: the identity service refused this engine.
	ErrLauncherRefused = errors.New("the vault identity service refused this engine")
	// ErrCustodianUnsupported: this platform has no native identity custodian.
	ErrCustodianUnsupported = errors.New("native credential storage is not implemented on this platform")
)

// DescribeCustody reports who holds the identity of the vault at vaultPath.
func DescribeCustody(vaultPath string) (CustodyReport, error) {
	if isolatedProcess() || !releaseBuild() {
		return CustodyReport{Mode: CustodyDevelopment}, nil
	}
	return describeCustody(filepath.Clean(vaultPath))
}
