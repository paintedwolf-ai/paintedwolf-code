// Package testutil holds shared test helpers. FailErr and its siblings turn raw
// errors and set-diff mismatches into one fatal labeled with the failing step.
package testutil

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// FailErr aborts when err != nil, prefixing the underlying error with a step label.
func FailErr(t testing.TB, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// SetDiff returns sorted members present in only one of want or got.
func SetDiff(want, got []string) (onlyWant, onlyGot []string) {
	mWant := make(map[string]struct{}, len(want))
	for _, v := range want {
		mWant[v] = struct{}{}
	}
	mGot := make(map[string]struct{}, len(got))
	for _, v := range got {
		mGot[v] = struct{}{}
	}
	for v := range mWant {
		if _, ok := mGot[v]; !ok {
			onlyWant = append(onlyWant, v)
		}
	}
	for v := range mGot {
		if _, ok := mWant[v]; !ok {
			onlyGot = append(onlyGot, v)
		}
	}
	sort.Strings(onlyWant)
	sort.Strings(onlyGot)
	return onlyWant, onlyGot
}

// FormatSetDiff renders a human-readable symmetric diff for string sets.
func FormatSetDiff(label string, want, got []string) string {
	onlyWant, onlyGot := SetDiff(want, got)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: set mismatch (%d expected, %d actual)", label, len(want), len(got))
	if len(onlyWant) > 0 {
		fmt.Fprintf(&b, "\n  only in expected: %s", strings.Join(onlyWant, ", "))
	}
	if len(onlyGot) > 0 {
		fmt.Fprintf(&b, "\n  only in actual:   %s", strings.Join(onlyGot, ", "))
	}
	if len(onlyWant) == 0 && len(onlyGot) == 0 {
		fmt.Fprintf(&b, "\n  same members but duplicate count differs")
		fmt.Fprintf(&b, "\n  expected: %v", want)
		fmt.Fprintf(&b, "\n  actual:   %v", got)
	}
	return b.String()
}
