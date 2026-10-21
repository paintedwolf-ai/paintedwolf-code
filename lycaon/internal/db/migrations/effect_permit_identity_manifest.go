package migrations

// Revision4 was the recorded-result data repair, with revision3's unchanged
// SQL shape. It is a known intermediate, not a newly claimed released baseline.
var effectPermitIdentitySource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentityImplementation,
		Checksum: "b87b293b39da775eb6fbd2ca69c0e43654755785e0dc0fbd1fd23c42ecf5ab44", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
