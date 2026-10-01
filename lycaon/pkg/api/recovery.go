package api

// Closed recovery_reason values on GET /health when status is "recovery".
const (
	RecoveryReasonIntegrityFailed = "integrity_failed"
	RecoveryReasonSchemaMismatch  = "schema_mismatch"
)

// HealthStatusRecovery is GET /health status when only restore routes are live.
const HealthStatusRecovery = "recovery"
