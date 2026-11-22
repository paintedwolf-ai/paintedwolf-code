package contract

import (
	"context"
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

func TestToolCommandOperationRoutesAllSixReachStates(t *testing.T) {
	rs, _, _, bp := effectiveOARBlockPlane(t)
	states, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)

	var callableMutating, deferredNoLoader *hoststate.TurnAuthority
	var forbiddenReadOnly []*hoststate.TurnAuthority
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
		case status == toolreach.Callable && !auth.Reach.Program("edit") && auth.Reach.Program("cat") && len(forbiddenReadOnly) < 3:
			copied := auth
			forbiddenReadOnly = append(forbiddenReadOnly, &copied)
		}
	}
	if callableMutating == nil || deferredNoLoader == nil || len(forbiddenReadOnly) == 0 {
		t.Fatalf("missing coordinator reach fixtures: callable=%v noLoader=%v readOnly=%d",
			callableMutating != nil, deferredNoLoader != nil, len(forbiddenReadOnly))
	}

	t.Run("1_command_callable_with_admitted_form", func(t *testing.T) {
		refusal := renderCommandOpRefusal(t, bp, &callableMutating.Reach, "edit")
		if !strings.Contains(refusal.Copy["cause"], "Routine operations use the command interface.") {
			t.Errorf("unexpected cause: %q", refusal.Copy["cause"])
		}
		if !strings.Contains(refusal.Copy["fix"], "`command(…)`") || !strings.Contains(refusal.Copy["fix"], "edit") {
			t.Errorf("fix must recommend admitted native form through command: %q", refusal.Copy["fix"])
		}
		if strings.Contains(refusal.Copy["fix"], "request_tools") {
			t.Errorf("callable command must not ask for request_tools: %q", refusal.Copy["fix"])
		}
	})

	t.Run("2_command_loadable_via_request_tools", func(t *testing.T) {
		readLoadable := toolreach.Freeze(
			[]string{toolreach.LoaderTool},
			toolsurface.Compile([]string{toolreach.LoaderTool}, []string{toolreach.ProgramTool}),
		).WithPrograms(forbiddenReadOnly[0].ProgramReach)
		readRef := renderCommandOpRefusal(t, bp, &readLoadable, "read")
		if fix := readRef.Copy["fix"]; !strings.Contains(fix, "`request_tools(need=\"command\")`") || !strings.Contains(fix, "`command(…)`") || !strings.Contains(fix, "cat") {
			t.Errorf("loadable read surface must instruct loading command first via request_tools: %q", fix)
		}
		mutLoadable := toolreach.Freeze(
			[]string{toolreach.LoaderTool},
			toolsurface.Compile([]string{toolreach.LoaderTool}, []string{toolreach.ProgramTool}),
		).WithPrograms(callableMutating.ProgramReach)
		editRef := renderCommandOpRefusal(t, bp, &mutLoadable, "edit")
		if fix := editRef.Copy["fix"]; !strings.Contains(fix, "`request_tools(need=\"command\")`") || !strings.Contains(fix, "`command(…)`") || !strings.Contains(fix, "edit") {
			t.Errorf("loadable edit reach must instruct loading command first via request_tools: %q", fix)
		}
	})

	t.Run("3_command_deferred_or_absent_without_loader", func(t *testing.T) {
		explicitDeferred := toolreach.Freeze([]string{"ask_user"}, toolsurface.Compile(nil, []string{"command"})).
			WithPrograms(func(string) bool { return true })
		for label, reach := range map[string]toolreach.Request{
			"coordinator_no_loader": deferredNoLoader.Reach,
			"deferred_no_loader":    explicitDeferred,
		} {
			refusal := renderCommandOpRefusal(t, bp, &reach, "read")
			if strings.Contains(refusal.Copy["fix"], "`command") || strings.Contains(refusal.Copy["fix"], "request_tools") {
				t.Errorf("%s recommended unreachable tool: %q", label, refusal.Copy["fix"])
			}
			if !strings.Contains(refusal.Copy["fix"], "Use an available capability or report the missing operation.") {
				t.Errorf("%s missing fallback guidance: %q", label, refusal.Copy["fix"])
			}
		}
	})

	t.Run("4_forbidden_effect_zero_admitted_forms", func(t *testing.T) {
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
		for i, auth := range forbiddenReadOnly {
			refusal := renderCommandOpRefusal(t, bp, &auth.Reach, "edit")
			if strings.Contains(refusal.Copy["fix"], "`command") {
				t.Errorf("read-only fixture[%d] recommended command for forbidden edit: %q", i, refusal.Copy["fix"])
			}
			if !strings.Contains(refusal.Copy["fix"], "Use an available capability or report the missing operation.") {
				t.Errorf("read-only fixture[%d] missing fallback guidance: %q", i, refusal.Copy["fix"])
			}
		}
	})

	t.Run("5_unknown_reach", func(t *testing.T) {
		refusal := renderCommandOpRefusal(t, bp, nil, "edit")
		if strings.Contains(refusal.Copy["fix"], "`command") || strings.Contains(refusal.Copy["fix"], "request_tools") {
			t.Errorf("unknown reach recommended command: %q", refusal.Copy["fix"])
		}
		if !strings.Contains(refusal.Copy["fix"], "Use an available capability or report the missing operation.") {
			t.Errorf("unknown reach missing fallback guidance: %q", refusal.Copy["fix"])
		}
	})

	t.Run("6_partial_multi_form_admission_and_step_fallback", func(t *testing.T) {
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
		t.Fatal("expected at least one multi-form command operation")
	}
	admittedSet := []string{allForms[0]}
	excluded := allForms[1:]
	partialReach := toolreach.Offered([]string{"command"}).WithPrograms(func(name string) bool {
		return slices.Contains(admittedSet, name)
	})
	refusal := renderCommandOpRefusal(t, bp, &partialReach, multiOp)
	fix := refusal.Copy["fix"]
	if !strings.Contains(fix, allForms[0]) || !strings.Contains(fix, "`command(…)`") {
		t.Errorf("partial multi-form fix missing admitted form %q: %q", allForms[0], fix)
	}
	for _, dropped := range excluded {
		if strings.Contains(fix, dropped) {
			t.Errorf("partial multi-form fix leaked unadmitted form %q: %q", dropped, fix)
		}
	}

	rule, ok := rs.Get("TOOL_COMMAND_OPERATION")
	if !ok || rule == nil {
		t.Fatal("TOOL_COMMAND_OPERATION missing from effective ruleset")
	}
	gcObserved := oar.NewGuardContext()
	gcObserved.Reach = deferredNoLoader.Reach
	gcObserved.ObservationData = map[string]any{"need": "edit"}
	gcObserved.ObserveCommandOperationReach()
	if got := oar.ResolveNextSteps(rule, gcObserved); got.Text != "" || len(got.Calls) != 0 {
		t.Fatalf("ObserveCommandOperationReach on unreachable deferred reach = %+v, want empty steps", got)
	}

	gc := oar.NewGuardContext()
	gc.Reach = deferredNoLoader.Reach
	gc.ObservationData = map[string]any{
		"need":                        "edit",
		"command_operation_reachable": true,
	}
	resolved := oar.ResolveNextSteps(rule, gc)
	if resolved.Text != "Use an available capability or report the missing operation." || len(resolved.Calls) != 0 {
		t.Fatalf("unreachable recovery step = %+v", resolved)
	}
}
