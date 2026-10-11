package migrations

// SourceNamespace preserves shipped history while adding directory identities and review assignments.
func SourceNamespace(target Baseline) Step {
	source := append(append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...), reviewAssignmentsMigration...)
	source = append(source, recoveryRetentionMigration...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "39cfefaa7223abd5d05b4a7c79e49b754780baf9ab46f1ba266c9d32bdca7a9d",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       target,
		Apply:    migrateSourceNamespace,
		Validate: validateSourceNamespace,
		// Reconstruction stays in the main SQLite database and uses its work reservation.
		ScratchBytes: 0,
	}
}
