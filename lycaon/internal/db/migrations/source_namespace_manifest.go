package migrations

// SourceNamespace preserves the shipped history while replacing its current-path projection.
func SourceNamespace() Step {
	source := append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "0ba15cd25656909e6f35ed9d542e2a346cd9f2b72b36c603212f0f144f5c2a07",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       Baseline{Revision: 2, Shape: "e6cf216656c2571aa402f58e5d5fb545283b7e4f5736afad06d299f1232b5e96"},
		Apply:    migrateSourceNamespace,
		Validate: validateSourceNamespace,
		// Reconstruction stays in the main SQLite database and uses its work reservation.
		ScratchBytes: 0,
	}
}
