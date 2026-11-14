package toolrejection

import (
	"errors"

	"github.com/lycaon/lycaon/internal/secretcap"
)

const (
	SecretReferenceNotFoundCode         = "SECRET_REFERENCE_NOT_FOUND"
	SecretReferenceOutOfScopeCode       = "SECRET_REFERENCE_OUT_OF_SCOPE"
	SecretReferenceRevokedCode          = "SECRET_REFERENCE_REVOKED"
	SecretReferenceAgentUseExpiredCode  = "SECRET_REFERENCE_AGENT_USE_EXPIRED"
	SecretReferenceValueUnavailableCode = "SECRET_REFERENCE_VALUE_UNAVAILABLE"
)

// SecretCapabilityReject maps a managed secret's lifecycle state that refuses
// its use. Other errors report false.
func SecretCapabilityReject(err error) (*ToolReject, bool) {
	switch {
	case errors.Is(err, secretcap.ErrNotFound):
		return &ToolReject{Code: SecretReferenceNotFoundCode, Retryable: true}, true
	case errors.Is(err, secretcap.ErrNotVisible):
		return &ToolReject{Code: SecretReferenceOutOfScopeCode, Retryable: true}, true
	case errors.Is(err, secretcap.ErrRevoked):
		return &ToolReject{Code: SecretReferenceRevokedCode, Retryable: true}, true
	case errors.Is(err, secretcap.ErrAgentUseExpired):
		return &ToolReject{Code: SecretReferenceAgentUseExpiredCode, Retryable: true}, true
	case errors.Is(err, secretcap.ErrValueMissing):
		return &ToolReject{Code: SecretReferenceValueUnavailableCode}, true
	}
	return nil, false
}

// IsSecretCapabilityCode reports whether code is a SecretCapabilityReject code.
func IsSecretCapabilityCode(code string) bool {
	switch code {
	case SecretReferenceNotFoundCode, SecretReferenceOutOfScopeCode, SecretReferenceRevokedCode,
		SecretReferenceAgentUseExpiredCode, SecretReferenceValueUnavailableCode:
		return true
	}
	return false
}
