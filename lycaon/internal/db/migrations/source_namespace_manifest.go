package migrations

// SourceNamespace preserves shipped history while adding directory identities and review assignments.
func SourceNamespace(target Baseline) Step {
	source := append(append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...), reviewAssignmentsMigration...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "71fec6ba812a71c6f74832311e59aba4e44b4cfcbe1b207f4adf9e8abd1b5151",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       target,
		Apply:    migrateSourceNamespace,
		Validate: validateSourceNamespace,
		// Reconstruction stays in the main SQLite database and uses its work reservation.
		ScratchBytes: 0,
	}
}
