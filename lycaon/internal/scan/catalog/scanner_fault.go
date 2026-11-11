package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// ConfigFaultReason classifies why a scanner contract cannot produce a runnable execution manifest.
type ConfigFaultReason string

const (
	ConfigFaultInvalidContract             ConfigFaultReason = "invalid_contract"
	ConfigFaultScannerNotSelected          ConfigFaultReason = "scanner_not_selected"
	ConfigFaultExecutableUnavailable       ConfigFaultReason = "executable_unavailable"
	ConfigFaultEngineIdentityMissing       ConfigFaultReason = "engine_identity_missing"
	ConfigFaultHostFloorIdentityIncomplete ConfigFaultReason = "host_floor_identity_incomplete"
	ConfigFaultSecretRulesIdentityMissing  ConfigFaultReason = "secret_rules_identity_missing"
)

// ConfigFaultError reports a permanent scanner configuration or engine fault.
type ConfigFaultError struct {
	ScannerID string
	Reason    ConfigFaultReason
	Detail    string
	Cause     error
}

func (e *ConfigFaultError) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}

func (e *ConfigFaultError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewConfigFaultError constructs a typed configuration fault error.
func NewConfigFaultError(scannerID string, reason ConfigFaultReason, detail string, cause error) error {
	if reason == "" {
		reason = ConfigFaultInvalidContract
	}
	return &ConfigFaultError{
		ScannerID: strings.TrimSpace(scannerID),
		Reason:    reason,
		Detail:    detail,
		Cause:     cause,
	}
}

// AsConfigFault extracts a typed configuration fault from err.
func AsConfigFault(err error) (*ConfigFaultError, bool) {
	var fault *ConfigFaultError
	if errors.As(err, &fault) && fault != nil {
		return fault, true
	}
	return nil, false
}

// IdentityDigest hashes the contract fields so callers can detect catalog or engine changes even when incomplete.
func (c ScannerContract) IdentityDigest() string {
	var b strings.Builder
	for _, part := range []string{
		c.AnalysisMode, c.ScannerID, c.Engine, c.Driver, string(c.Scope),
		c.DefinitionFingerprint, c.ParserID, c.MapperID, c.RulesSHA256,
		c.ExclusionsSHA256, c.EngineVersion, c.EngineSHA256, c.EngineExecutable,
	} {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
