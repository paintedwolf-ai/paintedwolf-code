package migrations

// Revision5Baseline is the shipped revision-5 shape.
var Revision5Baseline = Baseline{Revision: 5, Shape: "616ca6576c84b4d055b6eb519b1748977b93b8bb4664ab30658141c6d4aa5f14"}

// Revision6 adds Home submissions to the shipped revision 5 schema.
func Revision6(target Baseline) Step {
	return composeStep(Step{
		ID:       "home-starts",
		Checksum: homeStartsChecksum,
		From:     Revision5Baseline, To: target,
	}, HomeStarts())
}

const homeStartsChecksum = "54b5a8efe7ef02af00441abe44269fce6846e1f40cdbb3e92d6f2a2e988f9cac"
