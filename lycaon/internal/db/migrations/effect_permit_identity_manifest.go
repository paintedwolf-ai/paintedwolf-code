package migrations

// Revision6 is the main schema before portable native authority is added.
var effectPermitIdentitySource = Baseline{Revision: 6, Shape: "2294cfc726b056f7117798fd62673e13d995275b97f47f5ac2b8153a4a0c72dd"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentitySourceBytes,
		Checksum: "3c3862c529179e69f83a21dbd3b6d6bdf89017b92ee37bf90aa09fa39e6b6d58", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
