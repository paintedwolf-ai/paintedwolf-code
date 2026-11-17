package migrations

// revision5Release is the revision-5 shape the 1.0.3 release candidates ship.
var revision5Release = Baseline{Revision: 5, Shape: "bce201a54977c0bef851540d14bad604ffa672203b9837d757ace411fe7e2af6"}

// Revision5Release names the shipped revision-5 shape for the step that ends there.
func Revision5Release() Baseline { return revision5Release }

// Revision6 is the step from the shipped revision-5 shape. Until a build
// holding revision 6 is published, release candidates included, schema changes
// join these parts instead of advancing SchemaVersion.
func Revision6(target Baseline) Step {
	return composeStep(Step{
		ID:       "outside-namespace",
		Checksum: "c8caf73b1841ab94a58e1d318df7a30e1ebeaf753c5c962d580d32e6125ad645",
		From:     revision5Release, To: target,
	}, OutsideNamespace(), ChatLocations())
}
