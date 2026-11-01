package migrations

// Revision6 is the main schema before portable native authority is added.
var effectPermitIdentitySource = Baseline{Revision: 6, Shape: "2294cfc726b056f7117798fd62673e13d995275b97f47f5ac2b8153a4a0c72dd"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentitySourceBytes,
		Checksum: "417303ae6c704fdd6909312f57a37c41428182ced025d74d2c0cd4123fc89f95", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
