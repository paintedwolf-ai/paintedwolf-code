package contract

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/toolsurface"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
)

type contractNudgeFormatter struct {
	f *guidance.StaticRejectFormatter
}

func (n contractNudgeFormatter) FormatNudge(ctx context.Context, code string, data map[string]any) (string, error) {
	return guidance.FormatCoordinatorNudge(ctx, n.f, code, data)
}

func effectiveOARBlockPlane(t *testing.T) (*oar.RuleSet, *oar.GuardPipeline, *oar.Renderer, *toolfeedback.BlockPlane) {
	t.Helper()
	repoRoot := contractcheck.RepoRoot(t)
	schemaDir := filepath.Join(repoRoot, "schemas")
	contractcheck.FailErr(t, "install anchor catalog", anchorcatalog.InstallBundled())
	contractcheck.FailErr(t, "install OAR engine declaration", oar.InstallEngineDeclarationBundled(schemaDir))

	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load stock hint config", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "resolve effective pack catalog", err)

	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "new OAR loader", err)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(catalog)
	contractcheck.FailErr(t, "load effective OAR policy", err)

	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.SetDetectors(oar.HostDetectors(nil))
	for _, anchor := range []string{
		oar.AnchorToolHandler,
		oar.AnchorToolRejected,
		oar.AnchorToolPreInvoke,
		oar.AnchorSessionPreInvoke,
		oar.AnchorCoordinatorPreInvoke,
		oar.AnchorCoordinatorPostTurn,
		oar.AnchorCoordinatorCloseoutCheck,
		oar.AnchorCoordinatorMidTurnNote,
		oar.AnchorToolPost,
		oar.AnchorCredentialAssignment,
		oar.AnchorSharedTemp,
		oar.AnchorWorkerFinalize,
		oar.AnchorWorkerReportCheck,
		oar.AnchorContentInput,
		oar.AnchorContentOutput,
		oar.AnchorContentToolResult,
	} {
		pipeline.EnableAnchor(anchor)
	}
	renderer := oar.NewRenderer(formatter, contractNudgeFormatter{f: formatter})
	return rs, pipeline, renderer, &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: renderer}
}

func renderCommandOpRefusal(t *testing.T, bp *toolfeedback.BlockPlane, reach *toolreach.Request, need string) *guidance.Refusal {
	t.Helper()
	ctx := t.Context()
	if reach != nil {
		ctx = toolreach.WithRequest(ctx, *reach)
	}
	err := bp.RejectObservation(ctx, need, "coordinator", nil, &toolrejection.ToolReject{
		Code: "TOOL_COMMAND_OPERATION",
		Data: map[string]any{"need": need},
	})
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal == nil || refusal.Code() != "TOOL_COMMAND_OPERATION" {
		t.Fatalf("RejectObservation(%q) = %v, want TOOL_COMMAND_OPERATION refusal", need, err)
	}
	return refusal
}

// nextCallTools lists the tools of the refusal's delivered next-step calls.
func nextCallTools(ref *guidance.Refusal) []string {
	var out []string
	for _, c := range oarproducers.ObserveDeliveredRefusal(ref).NextCalls {
		out = append(out, c.Tool)
	}
	return out
}

// TOOL_COMMAND_OPERATION recovers through command only where command can run
// the operation: directly when callable, after loading when loadable, and
// never when command is out of reach or the operation's forms are forbidden.
func TestToolCommandOperationRecoveryFollowsCommandReach(t *testing.T) {
	rs, _, _, bp := effectiveOARBlockPlane(t)
	states, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)

	var callableMutating, deferredNoLoader *hoststate.TurnAuthority
	var forbiddenReadOnly []*hoststate.TurnAuthority
	seenReadOnlyReach := map[string]bool{}
	for _, st := range states {
		if st.Excluded {
			continue
		}
		auth, freezeErr := hoststate.FreezeTurnAuthority(t.Context(), st)
		contractcheck.FailErr(t, "FreezeTurnAuthority "+st.ID, freezeErr)
		status := auth.Reach.Status(toolreach.ProgramTool)
		switch {
		case callableMutating == nil && status == toolreach.Callable && auth.Reach.Program("edit"):
			copied := auth
			callableMutating = &copied
		case deferredNoLoader == nil && status == toolreach.Absent && auth.Reach.Status(toolreach.LoaderTool) == toolreach.Absent:
			copied := auth
			deferredNoLoader = &copied
		case status == toolreach.Callable && !auth.Reach.Program("edit") && auth.Reach.Program("cat"):
			// renderCommandOpRefusal only inspects auth.Reach (command/loader reach and admitted programs).
			rk := fmt.Sprintf("%d|%d|%s|%s",
				status, auth.Reach.Status(toolreach.LoaderTool),
				strings.Join(auth.Offered, ","), strings.Join(auth.Plan.DeferredNames(), ","))
			if !seenReadOnlyReach[rk] {
				seenReadOnlyReach[rk] = true
				copied := auth
				forbiddenReadOnly = append(forbiddenReadOnly, &copied)
			}
		}
	}
	if callableMutating == nil || deferredNoLoader == nil || len(forbiddenReadOnly) == 0 {
		t.Fatalf("missing coordinator reach states: callable=%v noLoader=%v readOnly=%d",
			callableMutating != nil, deferredNoLoader != nil, len(forbiddenReadOnly))
	}

	t.Run("command_callable_with_admitted_form", func(t *testing.T) {
		got := nextCallTools(renderCommandOpRefusal(t, bp, &callableMutating.Reach, "edit"))
		if !slices.Contains(got, "command") || slices.Contains(got, toolreach.LoaderTool) {
			t.Errorf("callable command next calls = %v, want command without loading", got)
		}
	})

	t.Run("command_loadable", func(t *testing.T) {
		plan := toolsurface.Compile([]string{toolreach.LoaderTool}, []string{toolreach.ProgramTool})
		for need, programs := range map[string]toolreach.Programs{
			"read": forbiddenReadOnly[0].ProgramReach,
			"edit": callableMutating.ProgramReach,
		} {
			reach := toolreach.Freeze([]string{toolreach.LoaderTool}, plan).WithPrograms(programs)
			got := nextCallTools(renderCommandOpRefusal(t, bp, &reach, need))
			if !slices.Contains(got, toolreach.LoaderTool) || !slices.Contains(got, "command") {
				t.Errorf("loadable command %s next calls = %v, want loading then command", need, got)
			}
		}
	})

	t.Run("command_out_of_reach", func(t *testing.T) {
		explicitDeferred := toolreach.Freeze([]string{"ask_user"}, toolsurface.Compile(nil, []string{"command"})).
			WithPrograms(func(string) bool { return true })
		workerStates, wErr := hoststate.WorkerStates(t.Context())
		contractcheck.FailErr(t, "WorkerStates", wErr)
		for _, ws := range workerStates {
			if ws.Excluded || ws.ToolProfile != "explore_readonly" {
				continue
			}
			wAuth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
			contractcheck.FailErr(t, "FreezeWorkerAuthority", fErr)
			forbiddenReadOnly = append(forbiddenReadOnly, &wAuth)
			break
		}
		cases := map[string]struct {
			reach *toolreach.Request
			need  string
		}{
			"coordinator_no_loader": {&deferredNoLoader.Reach, "read"},
			"deferred_no_loader":    {&explicitDeferred, "read"},
			"unknown_reach":         {nil, "edit"},
		}
		for i, auth := range forbiddenReadOnly {
			cases[fmt.Sprintf("read_only_%d", i)] = struct {
				reach *toolreach.Request
				need  string
			}{&auth.Reach, "edit"}
		}
		for label, tc := range cases {
			if got := nextCallTools(renderCommandOpRefusal(t, bp, tc.reach, tc.need)); slices.Contains(got, "command") || slices.Contains(got, toolreach.LoaderTool) {
				t.Errorf("%s next calls = %v, want no command or loading", label, got)
			}
		}
	})

	t.Run("partial_multi_form_admission", func(t *testing.T) {
		verifyPartialMultiFormAndStepFallback(t, rs, bp, deferredNoLoader)
	})
}

func verifyPartialMultiFormAndStepFallback(
	t *testing.T,
	rs *oar.RuleSet,
	bp *toolfeedback.BlockPlane,
	deferredNoLoader *hoststate.TurnAuthority,
) {
	t.Helper()
	ops, opsErr := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "CommandOperations", opsErr)
	var multiOp string
	var allForms []string
	for _, op := range ops {
		if forms, ok := toolcontract.RetiredCommandForms(op); ok && len(forms) >= 2 {
			multiOp, allForms = op, forms
			break
		}
	}
	if multiOp == "" {
		t.Fatal("no multi-form command operation")
	}
	partialReach := toolreach.Offered([]string{"command"}).WithPrograms(func(name string) bool {
		return name == allForms[0]
	}).WithSchemas(map[string]map[string]any{"command": {"properties": map[string]any{"command": map[string]any{"type": "string"}}}})
	for _, c := range oarproducers.ObserveDeliveredRefusal(renderCommandOpRefusal(t, bp, &partialReach, multiOp)).NextCalls {
		if c.Tool != "command" {
			continue
		}
		cmd, _ := c.Args["command"].(string)
		for _, dropped := range allForms[1:] {
			if c.Program == dropped || strings.HasPrefix(cmd, dropped+" ") || cmd == dropped {
				t.Errorf("partial multi-form next call runs unadmitted form %q: %+v", dropped, c)
			}
		}
	}

	rule, ok := rs.Get("TOOL_COMMAND_OPERATION")
	if !ok || rule == nil {
		t.Fatal("TOOL_COMMAND_OPERATION missing from effective ruleset")
	}
	for _, data := range []map[string]any{
		{"need": "edit"},
		{"need": "edit", "command_operation_reachable": true},
	} {
		gc := oar.NewGuardContext()
		gc.Reach = deferredNoLoader.Reach
		gc.ObservationData = data
		if _, observed := data["command_operation_reachable"]; !observed {
			gc.ObserveCommandOperationReach()
		}
		if got := oar.ResolveNextSteps(context.Background(), rule, gc); len(got.Calls) != 0 {
			t.Errorf("unreachable command with %v resolved next calls %+v", data, got.Calls)
		}
	}
}
