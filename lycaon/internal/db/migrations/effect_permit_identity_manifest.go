package migrations

// EffectPermitIdentity adds native approval authority to the shipped approval records.
func EffectPermitIdentity() Part {
	return Part{Source: effectPermitIdentitySourceBytes, Apply: migrateEffectPermitIdentity}
}
