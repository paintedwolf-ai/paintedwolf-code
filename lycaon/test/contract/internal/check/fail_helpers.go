package check

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func FailErr(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

func FailSetEqual(t *testing.T, label string, want, got []string) {
	t.Helper()
	if SortedSetEqual(want, got) {
		return
	}
	t.Fatal(testutil.FormatSetDiff(label, want, got))
}

func FailViolations(t *testing.T, heading string, violations []string) {
	t.Helper()
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", heading, len(violations))
	for _, violation := range violations {
		b.WriteString("  - ")
		b.WriteString(violation)
		b.WriteByte('\n')
	}
	t.Fatal(b.String())
}
