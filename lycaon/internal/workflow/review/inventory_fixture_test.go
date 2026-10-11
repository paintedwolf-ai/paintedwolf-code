package review_test

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeInventory struct {
	run   []api.CodeScan
	other map[string]api.CodeScan
}

func (f fakeInventory) RunScans(context.Context, string) ([]api.CodeScan, error) { return f.run, nil }

func (f fakeInventory) Scan(_ context.Context, id string) (*api.CodeScan, error) {
	for _, s := range f.run {
		if s.ID == id {
			return &s, nil
		}
	}
	if s, ok := f.other[id]; ok {
		return &s, nil
	}
	return nil, nil
}

func secretFinding(path string) api.SecurityFinding {
	return api.SecurityFinding{
		RuleID: "gitleaks:generic-api-key", Level: api.FindingLevelHigh,
		Locations: []api.SecurityFindingLocation{{URI: path}},
		Tool:      api.ToolDescriptor{DriverID: "lycaon-secrets"},
	}
}

// A cited scan id is always wrong, and the refusal names the groups that scan
// holds in the run; an unknown id is wrong once the run's scans are terminal.
