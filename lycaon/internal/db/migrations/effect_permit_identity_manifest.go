package migrations

// Revision6 is the main schema before portable native authority is added.
var effectPermitIdentitySource = Baseline{Revision: 6, Shape: "2294cfc726b056f7117798fd62673e13d995275b97f47f5ac2b8153a4a0c72dd"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentitySourceBytes,
		Checksum: "09b6b992790356976cf0f833185fc6b5909107822fad308477b179a2ddb31d2b", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
