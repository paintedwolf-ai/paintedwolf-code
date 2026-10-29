package migrations

// Revision6 is the main schema before portable native authority is added.
var effectPermitIdentitySource = Baseline{Revision: 6, Shape: "2294cfc726b056f7117798fd62673e13d995275b97f47f5ac2b8153a4a0c72dd"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentitySourceBytes,
		Checksum: "13848cc7b59739e3971bd0a793d996372bde110f0b6b4e9bb14a9925697557a5", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
