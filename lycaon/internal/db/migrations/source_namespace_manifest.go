package migrations

// SourceNamespace preserves shipped history while adding directory identities and review assignments.
func SourceNamespace(target Baseline) Step {
	source := append(append(append(append([]byte{}, namespaceImplementation...), namespaceSchema...), namespaceProjection...), reviewAssignmentsMigration...)
	return Step{
		ID:       "source-directory-identity",
		Source:   source,
		Checksum: "fee1a1e3430b156cbd2b151af5a7673c5c17d99dc58cdae17754dda2f6759f39",
		From:     Baseline{Revision: 1, Shape: "4c183bce0a7e107596c03a44a68b384cf216b3932ddf31e17d95f400772f478a"},
		To:       target,
		Apply:    migrateSourceNamespace,
		Validate: validateSourceNamespace,
		// All reconstruction lives in the main SQLite database, covered by the
		// caller's database work reservation. No external scratch files are used.
		ScratchBytes: 0,
	}
}
