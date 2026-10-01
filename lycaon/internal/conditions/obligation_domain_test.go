package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubObligationStatus struct {
	status string
}

func (s stubObligationStatus) Status(context.Context, string, string) (api.WorkflowRunObligation, error) {
	return api.WorkflowRunObligation{Status: s.status}, nil
}

func TestObligationSettled(t *testing.T) {
	tests := []struct {
		name   string
		status string
		vars   map[string]any
		want   bool
	}{
		{name: "off", status: api.ObligationStatusOff, want: true},
		{name: "complete", status: api.ObligationStatusComplete, want: true},
		{name: "failed", status: api.ObligationStatusFailed, want: true},
		{name: "pending", status: api.ObligationStatusPending},
		{name: "empty", status: api.ObligationStatusEmpty},
		{
			name:   "enqueue failure",
			status: api.ObligationStatusEmpty,
			vars: map[string]any{"obligations": map[string]any{
				"scan": map[string]any{"status": api.ObligationStatusFailed},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := conditions.NewRegistry()
			err := conditions.RegisterObligationDomain(reg, map[string]conditions.ObligationStatusReader{
				"scan": stubObligationStatus{status: tt.status},
			})
			testutil.FailErr(t, "register obligation domain", err)
			got, err := reg.Evaluate("obligation_settled:scan", conditions.EvalContext{
				Ctx:           context.Background(),
				WorkflowRunID: "run-1",
				Vars:          tt.vars,
			})
			testutil.FailErr(t, "evaluate obligation gate", err)
			if got != tt.want {
				t.Fatalf("settled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestObligationSettledRequiresRegisteredKindAndRun(t *testing.T) {
	reg := conditions.NewRegistry()
	err := conditions.RegisterObligationDomain(reg, map[string]conditions.ObligationStatusReader{
		"scan": stubObligationStatus{status: api.ObligationStatusComplete},
	})
	testutil.FailErr(t, "register obligation domain", err)

	for _, tc := range []struct {
		leaf  string
		runID string
	}{
		{leaf: "obligation_settled:missing", runID: "run-1"},
		{leaf: "obligation_settled:scan"},
	} {
		got, err := reg.Evaluate(tc.leaf, conditions.EvalContext{Ctx: context.Background(), WorkflowRunID: tc.runID})
		testutil.FailErr(t, "evaluate obligation gate", err)
		if got {
			t.Fatalf("%s settled without a registered kind and run", tc.leaf)
		}
	}
}
