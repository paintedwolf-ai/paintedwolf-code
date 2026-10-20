package migrations

// Revision4 was the recorded-result data repair, with revision3's unchanged
// SQL shape. It is a known intermediate, not a newly claimed released baseline.
var effectPermitIdentitySource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentityImplementation,
		Checksum: "8aa0dae13a94b689eb77061b2c53147b02345bdca704b0ae6d33f80d8a5ebaf5", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
