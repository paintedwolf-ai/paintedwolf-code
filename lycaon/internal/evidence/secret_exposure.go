package evidence

import "strings"

// Ledger-only secret-exposure markers (no corresponding tool-result message).
const (
	SecretExposureHandlePrefix  = "secret_exposure#"
	InheritSecretExposureHandle = "secret_exposure_inherit#1"
)

// RecordMarksSecretExposure checks host-authored credential markers.
func RecordMarksSecretExposure(rec Record) bool {
	h := terminalHandle(rec.Handle)
	return h == InheritSecretExposureHandle || strings.HasPrefix(h, SecretExposureHandlePrefix)
}
