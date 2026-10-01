package sessionadmin

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestValidSessionPosture(t *testing.T) {
	cases := []struct {
		mode  wire.SessionPosture
		valid bool
	}{
		{wire.SessionPostureSpec, true},
		{wire.SessionPostureVet, true},
		{wire.SessionPostureOrchestrate, true},
		{wire.SessionPostureBuild, true},
		{wire.SessionPosture("bogus"), false},
	}
	for _, tc := range cases {
		if got := validSessionPosture(tc.mode); got != tc.valid {
			t.Fatalf("validSessionPosture(%q) = %v, want %v", tc.mode, got, tc.valid)
		}
	}
}
