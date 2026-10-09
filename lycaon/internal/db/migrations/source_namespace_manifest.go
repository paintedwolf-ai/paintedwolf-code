package migrations

// SourceNamespace preserves the shipped history while replacing its current-path projection.
func SourceNamespace() Step {
	source := append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "782558018b0dcaa066db5edecbfd04f4df2e3a07e1d490536fcf5e3b4fe9ecae",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       Baseline{Revision: 2, Shape: "e6cf216656c2571aa402f58e5d5fb545283b7e4f5736afad06d299f1232b5e96"},
		Apply:    migrateSourceNamespace,
	}
}
