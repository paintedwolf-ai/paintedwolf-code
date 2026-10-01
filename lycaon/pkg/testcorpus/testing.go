package testcorpus

// TestingT supports corpus assertions.
type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// RequireNonEmpty prevents vacuous corpus assertions.
func RequireNonEmpty[T any](t TestingT, label string, items []T) []T {
	t.Helper()
	if len(items) == 0 {
		t.Fatalf("%s: source corpus selection is empty", label)
	}
	return items
}
