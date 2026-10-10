package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSharedObservationFactsRegistered(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{}
	for _, fact := range oar.FactCatalogue() {
		declared[fact.Name] = true
	}
	for _, tc := range []struct {
		name, condition string
		observe         func(*oar.GuardContext)
	}{
		{"is_directory", "is_directory", func(gc *oar.GuardContext) { gc.Rejection.IsDirectory = true }},
		{"not_found", "not_found", func(gc *oar.GuardContext) { gc.Rejection.NotFound = true }},
		{"path_denied", "path_denied", func(gc *oar.GuardContext) { gc.Rejection.PathDenied = true }},
		{"reject_observation", "paintedwolf.reject_observation == 'path_escape'", func(gc *oar.GuardContext) { gc.Rejection.RejectObservation = "path_escape" }},
		{"policy_denied", "policy_denied", func(gc *oar.GuardContext) { gc.Rejection.PolicyDenied = true }},
		{"command_not_argv", "paintedwolf.command_not_argv", func(gc *oar.GuardContext) { gc.Invocation.CommandNotArgv = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !declared[tc.name] {
				t.Fatalf("catalogue missing %q", tc.name)
			}
			gc := oar.NewGuardContext()
			matched, err := oar.EvaluateCondition(tc.condition, gc)
			contractcheck.FailErr(t, "evaluate absent observation", err)
			if matched {
				t.Fatal("unobserved rejection fact matched")
			}
			tc.observe(gc)
			matched, err = oar.EvaluateCondition(tc.condition, gc)
			contractcheck.FailErr(t, "evaluate structured observation", err)
			if !matched {
				t.Fatal("observed rejection fact is absent from the condition environment")
			}
		})
	}
}
