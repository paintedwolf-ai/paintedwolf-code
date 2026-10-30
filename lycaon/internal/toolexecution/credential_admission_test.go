package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type unavailableCredentialIndex struct {
	project string
	failure error
}

func (i *unavailableCredentialIndex) EnsureProject(_ context.Context, project string) error {
	i.project = project
	return i.failure
}
func (*unavailableCredentialIndex) ForgetProject(context.Context, string) error { return nil }
func (*unavailableCredentialIndex) Delivered(context.Context, tools.CredentialFileRead) error {
	return nil
}
func (*unavailableCredentialIndex) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}

func TestCredentialFileAdmissionPrecedesToolDispatch(t *testing.T) {
	for _, failure := range []error{errors.New("capture unavailable"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			registry := tools.NewDefaultRegistry()
			dispatched := false
			registerTestDefinition(t, registry, "probe", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				dispatched = true
				return "unexpected", nil
			})
			executor := NewExecutor(nil, registry, "implement")
			index := &unavailableCredentialIndex{failure: failure}
			tc := fixtureToolContext(t.TempDir())
			tc.Identity.ProjectID = "project"
			tc.Effects.CredentialFiles = index
			output, err := executor.Invoke(t.Context(), "probe", map[string]any{}, tc)
			reject := toolrejection.AsToolReject(err)
			if output != "" || dispatched || index.project != "project" || reject == nil || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != secretmatch.FaultStageManagedSecretStore {
				t.Fatalf("output=%q dispatched=%v project=%q reject=%+v error=%v", output, dispatched, index.project, reject, err)
			}
		})
	}
}
