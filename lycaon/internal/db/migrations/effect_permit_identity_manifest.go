package migrations

// Revision4 was the recorded-result data repair, with revision3's unchanged
// SQL shape. It is a known intermediate, not a newly claimed released baseline.
var effectPermitIdentitySource = Baseline{Revision: 4, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

func EffectPermitIdentity(target Baseline) Step {
	return Step{ID: "native-child-once-authority", Source: effectPermitIdentityImplementation,
		Checksum: "cbc325b659a4f98f27a596ebc33018b62a8fb5e5339e43391719c151fb80cf4a", From: effectPermitIdentitySource, To: target, Apply: migrateEffectPermitIdentity}
}
