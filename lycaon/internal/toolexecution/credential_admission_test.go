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

func (*unavailableCredentialIndex) Start(string) {}
func (i *unavailableCredentialIndex) Await(_ context.Context, wait tools.CredentialWait) error {
	i.project = wait.ProjectID
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
	for _, mode := range []struct {
		name    string
		program bool
	}{{"tool", false}, {"program", true}} {
		for _, failure := range []error{errors.New("capture unavailable"), context.Canceled} {
			t.Run(mode.name+"/"+failure.Error(), func(t *testing.T) {
				registry := tools.NewDefaultRegistry()
				dispatched := false
				registerTestDefinition(t, registry, "probe", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
					dispatched = true
					return "unexpected", nil
				})
				executor := NewExecutor(nil, registry, "implement")
				if mode.program {
					executor.programEntries = map[string]func(map[string]any) bool{"probe": nil}
				}
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
}

func TestCredentialFileAdmissionReturnsCallerCancellation(t *testing.T) {
	for _, program := range []bool{false, true} {
		registry := tools.NewDefaultRegistry()
		registerTestDefinition(t, registry, "probe", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return "unexpected", nil
		})
		executor := NewExecutor(nil, registry, "implement")
		if program {
			executor.programEntries = map[string]func(map[string]any) bool{"probe": nil}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tc := fixtureToolContext(t.TempDir())
		tc.Identity.ProjectID = "project"
		tc.Effects.CredentialFiles = &unavailableCredentialIndex{failure: context.Canceled}
		output, err := executor.Invoke(ctx, "probe", map[string]any{}, tc)
		if output != "" || !errors.Is(err, context.Canceled) || toolrejection.AsToolReject(err) != nil {
			t.Fatalf("program=%v output=%q error=%v", program, output, err)
		}
	}
}

func TestCredentialFileAdmissionAllowsReadyIndexForBothEntries(t *testing.T) {
	for _, program := range []bool{false, true} {
		registry := tools.NewDefaultRegistry()
		dispatched := false
		registerTestDefinition(t, registry, "probe", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			dispatched = true
			return "dispatched", nil
		})
		executor := NewExecutor(nil, registry, "implement")
		if program {
			executor.programEntries = map[string]func(map[string]any) bool{"probe": nil}
		}
		index := &unavailableCredentialIndex{}
		tc := fixtureToolContext(t.TempDir())
		tc.Identity.ProjectID = "project"
		tc.Effects.CredentialFiles = index
		output, err := executor.Invoke(t.Context(), "probe", map[string]any{}, tc)
		if err != nil || output != "dispatched" || !dispatched || index.project != "project" {
			t.Fatalf("program=%v output=%q dispatched=%v project=%q error=%v", program, output, dispatched, index.project, err)
		}
	}
}
