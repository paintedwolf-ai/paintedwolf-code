package api

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestValidHuntStrategy(t *testing.T) {
	cases := []struct {
		in   wire.HuntStrategy
		want bool
	}{
		{wire.HuntStrategyFileBased, true},
		{wire.HuntStrategyFeatureBased, true},
		{wire.HuntStrategyRiskBased, true},
		{wire.HuntStrategyResearchBased, true},
		{wire.HuntStrategy(""), false},
		{wire.HuntStrategy("single"), false},
		{wire.HuntStrategy("FILE-BASED"), false},
		{wire.HuntStrategy("file_based"), false},
	}
	for _, tc := range cases {
		if got := validHuntStrategy(tc.in); got != tc.want {
			t.Errorf("validHuntStrategy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidInspectMode(t *testing.T) {
	cases := []struct {
		in   wire.InspectMode
		want bool
	}{
		{wire.InspectModeStandard, true},
		{wire.InspectModeTurbo, true},
		{wire.InspectModeFull, true},
		{wire.InspectMode(""), false},
		{wire.InspectMode("fast"), false},
	}
	for _, tc := range cases {
		if got := validInspectMode(tc.in); got != tc.want {
			t.Errorf("validInspectMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
