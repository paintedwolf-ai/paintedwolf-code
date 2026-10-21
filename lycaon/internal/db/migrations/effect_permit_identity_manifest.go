package migrations

// Revision4 was the recorded-result data repair, with revision3's unchanged
// SQL shape. It is a known intermediate, not a newly claimed released baseline.
var effectPermitIdentitySource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentityStepSource,
		Checksum: "5398f75fa3b311e22d0f95a235ce75b3538ecf721307c6a8a23f6923f8d1982d", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
