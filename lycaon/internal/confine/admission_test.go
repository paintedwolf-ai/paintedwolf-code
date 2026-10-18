package confine

import (
	"errors"
	"testing"
)

func TestRequireAppliedHonorsExplicitSandboxPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		auto     bool
		sandbox  string
		bypass   string
		applied  bool
		wantStop bool
	}{
		{name: "required without boundary", auto: true, wantStop: true},
		{name: "applied", auto: true, applied: true},
		{name: "explicit sandbox off", auto: true, sandbox: "off"},
		{name: "explicit bypass", auto: true, bypass: "1"},
		{name: "not enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := autoConfineEnabled()
			setAutoConfine(tc.auto)
			t.Cleanup(func() { setAutoConfine(previous) })
			t.Setenv("LYCAON_SANDBOX", tc.sandbox)
			t.Setenv("LYCAON_BYPASS_APPROVALS", tc.bypass)
			err := RequireApplied(tc.applied)
			if errors.Is(err, ErrNotConfined) != tc.wantStop || (err != nil && !tc.wantStop) {
				t.Fatalf("RequireApplied(%v) = %v, want refusal %v", tc.applied, err, tc.wantStop)
			}
		})
	}
}
