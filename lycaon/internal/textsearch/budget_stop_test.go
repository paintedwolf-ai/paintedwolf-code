package textsearch

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestScanBudgetStopNamesItsRecord(t *testing.T) {
	limits := catalogLimits(t)
	limits.WorkSteps = 1 << 12
	m, err := Compile(t.Context(), Spec{Patterns: []string{`b`}, Dialect: Extended}, limits)
	testutil.FailErr(t, "compile", err)
	long := strings.Repeat("a", 1<<13)
	input := "b\n" + long + "\nb\n"
	_, err = ScanReader(t.Context(), bytes.NewReader([]byte(input)), 0, nil, m, ScanOptions{MaxSelected: -1}, func(Line) error { return nil })
	var budget *BudgetError
	if !errors.As(err, &budget) || budget.Line != 2 || budget.RecordBytes != int64(len(long)) {
		t.Fatalf("stop = %+v (%v)", budget, err)
	}
}

func TestMatcherLiteral(t *testing.T) {
	limits := catalogLimits(t)
	for _, tc := range []struct {
		spec    Spec
		literal bool
	}{
		{Spec{Patterns: []string{"a.b"}, Dialect: Fixed}, true},
		{Spec{Patterns: []string{"x[0-9]*y"}, Dialect: Extended}, false},
	} {
		m, err := Compile(t.Context(), tc.spec, limits)
		testutil.FailErr(t, "compile", err)
		if m.Literal() != tc.literal {
			t.Fatalf("%+v: literal = %v", tc.spec, m.Literal())
		}
	}
}
