package migrations

// SourceNamespace preserves shipped history while adding directory identities and review assignments.
func SourceNamespace(target Baseline) Step {
	source := append(append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...), reviewAssignmentsMigration...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "b914229b9d7901f3a941e70855df1f8b6d93ceeffde62e26c5b618047d14b434",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       target,
		Apply:    migrateSourceNamespace,
		Validate: validateSourceNamespace,
		// Reconstruction stays in the main SQLite database and uses its work reservation.
		ScratchBytes: 0,
	}
}
