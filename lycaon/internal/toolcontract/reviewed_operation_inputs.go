package toolcontract

// ReviewedOperationInputs freezes the actual reviewed opaque-reference object.
// Resolved secret values belong only to the ephemeral owner dispatch context.
// Admission retains this immutable snapshot under the existing state lease.
type ReviewedOperationInputs struct {
	ArgumentsDigest    string
	CanonicalArguments string
}
