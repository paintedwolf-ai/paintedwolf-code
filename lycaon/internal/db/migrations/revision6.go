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
		Checksum: "af32b308c4b34cf56c2fb8c512b112c5b1fca8f7a20a9c9f2f9784034acaa96b",
		From:     revision5Release, To: target,
	}, OutsideNamespace())
}
