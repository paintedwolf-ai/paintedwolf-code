package oswalk

import (
	"errors"
	"testing"
)

// TestSkipAlwaysReturnsNil: Skip swallows per-entry walk errors so
// filepath.WalkDir keeps walking; a non-nil return would abort every walk that
// routes its errors through it.
func TestSkipAlwaysReturnsNil(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   error
	}{
		{"nil input", nil},
		{"non-nil input", errors.New("permission denied")},
		{"wrapped error", errors.Join(errors.New("a"), errors.New("b"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Skip(tc.in); got != nil {
				t.Fatalf("Skip(%v) = %v, want nil — walk callbacks rely on this", tc.in, got)
			}
		})
	}
}
