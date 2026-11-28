package promptloop

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

type argValidationLedger struct {
	mu       sync.Mutex
	receipts map[string]*api.InvocationReceipt
}

func newArgValidationLedger() *argValidationLedger {
	return &argValidationLedger{receipts: map[string]*api.InvocationReceipt{}}
}

func (l *argValidationLedger) Begin(_ context.Context, in invocation.Start) (*api.InvocationReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := &api.InvocationReceipt{
		ID: uuid.NewString(), Tool: in.ToolName, ToolCallID: in.ToolCallID,
		Owner: in.Contract.Owner, Lifecycle: in.Contract.Lifecycle.String(),
		EvidencePolicy: string(in.Contract.Evidence()), Status: api.InvocationStatusRunning,
	}
	l.receipts[r.ID] = r
	return r, nil
}

func (l *argValidationLedger) Settle(_ context.Context, id string, in invocation.Settlement) (*api.InvocationReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.receipts[id]
	r.Status = in.Status
	r.Invoked = in.Invoked
	r.Failure = in.Failure
	return r, nil
}

func (l *argValidationLedger) ListSession(context.Context, string) ([]api.InvocationReceipt, error) {
	return nil, nil
}

func (l *argValidationLedger) ListSessionPage(context.Context, string, string, int) (api.InvocationReceiptList, error) {
	return api.InvocationReceiptList{}, nil
}

func (l *argValidationLedger) InterruptRunning(context.Context) (int64, error) { return 0, nil }

func setupArgValidationLoop(
	t *testing.T,
	reg tools.ToolRegistry,
	ledger *argValidationLedger,
	observed *[]*oar.GuardContext,
) (*PromptLoop, *oar.CounterStore) {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	testutil.FailErr(t, "install oar profile", oar.InstallEngineDeclarationBundled(filepath.Join(root, "schemas")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create oar loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load effective policy", err)
	counters := oar.NewCounterStore()
	pipeline := oar.NewGuardPipeline(rules, loader, counters)
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.SetFactProvider("paintedwolf.arg_validation_errors", func(gc *oar.GuardContext) error {
		*observed = append(*observed, gc)
		return nil
	})
	// Every tested rule reads rejection_code, so this provider sees each GuardContext.
	pipeline.SetFactProvider("paintedwolf.rejection_code", func(gc *oar.GuardContext) error {
		*observed = append(*observed, gc)
		return nil
	})
	hintCfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint config", err)
	fmtter := guidance.NewStaticRejectFormatter(hintCfg)
	bp := &toolfeedback.BlockPlane{
		Pipeline: pipeline,
		Renderer: oar.NewRenderer(fmtter, nil),
	}
	return NewPromptLoopForTest(PromptLoopDeps{
		Context:  ContextDeps{Tools: reg},
		Tools:    ToolsDeps{BlockPlane: bp, Invocations: ledger},
		Closeout: CloseoutDeps{RejectFmt: fmtter, HintConfig: hintCfg},
	}), counters
}

func TestValidateCallArgumentsPreservesTypedRejectThroughBlockPlane(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	commandContract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("missing compiled command contract")
	}
	commandSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"capability_request": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"write_root": map[string]any{"type": "string"},
				},
			},
		},
	}
	testutil.FailErr(t, "register command", reg.RegisterDefinition(tools.Definition{
		Meta:     tools.ToolMeta{Name: "command", ArgsSchema: commandSchema},
		Contract: commandContract,
		Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("invalid arguments reached handler")
			return "", nil
		},
	}))
	writeContract, ok := toolcontract.Lookup("write")
	if !ok {
		t.Fatal("missing compiled write contract")
	}
	testutil.FailErr(t, "register write", reg.RegisterDefinition(tools.Definition{
		Meta: tools.ToolMeta{
			Name:       "write",
			ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}},
		},
		Contract: writeContract,
		Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("truncated write reached handler")
			return "", nil
		},
	}))
	brokenContract := toolcontract.External("mcp:broken")
	testutil.FailErr(t, "register broken schema tool", reg.RegisterDefinition(tools.Definition{
		Meta: tools.ToolMeta{
			Name:       "mcp_broken_schema",
			ArgsSchema: map[string]any{"type": "object"},
		},
		Contract: brokenContract,
		Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("broken schema tool reached handler")
			return "", nil
		},
	}))

	var observed []*oar.GuardContext
	ledger := newArgValidationLedger()
	loop, _ := setupArgValidationLoop(t, reg, ledger, &observed)
	sess := &api.Session{
		ID:            "sess-arg-curation",
		ProjectID:     "proj-1",
		OwnerPersonID: "person-1",
		Posture:       api.SessionPostureBuild,
		Mode:          api.SessionModeAsk,
	}
	offered := []string{"command", "write", "mcp_broken_schema"}
	prunedAskCommandSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"command"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
		},
	}
	baseToolCtx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: sess.ProjectID, Agent: "coordinator"},
		Turn: tools.InvocationTurn{
			TurnSurfaceID:        "implement_investigate",
			TurnOfferedToolNames: offered,
			TurnToolPlan:         toolsurface.Compile(offered, nil),
			TurnOfferedToolSchemas: map[string]map[string]any{
				"command":           prunedAskCommandSchema,
				"mcp_broken_schema": {"type": "object", "$ref": "#/$defs/missing_schema_def"},
			},
		},
	}

	assertTruncatedAndMalformedCuration(t, loop, sess, baseToolCtx, commandContract, &observed)
	assertInvalidAndOwnerFailedCuration(t, loop, sess, baseToolCtx, brokenContract, &observed)
}

func assertTruncatedAndMalformedCuration(
	t *testing.T,
	loop *PromptLoop,
	sess *api.Session,
	baseToolCtx tools.ToolContext,
	commandContract toolcontract.Contract,
	observed *[]*oar.GuardContext,
) {
	t.Helper()
	t.Run("truncated_preserves_chunkable_fact_and_counters", func(t *testing.T) {
		*observed = nil
		run := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-trunc-1", Name: "command", Args: map[string]any{}, ArgsTruncated: true,
		}, baseToolCtx, nil, 0, "msg-1", api.CoordinatorRunContext{})
		if run.reject == nil || run.reject.Code() != "TOOL_ARGS_TRUNCATED" {
			t.Fatalf("reject = %+v, want TOOL_ARGS_TRUNCATED", run.reject)
		}
		cause := toolrejection.AsToolReject(run.reject)
		if cause == nil || !cause.ArgumentValidation || cause.FailureClass != api.FailureClassHostRejection {
			t.Fatalf("cause = %+v, want ArgumentValidation=true and host_rejection", cause)
		}
		if run.receipt == nil || run.contract.Digest() != commandContract.Digest() {
			t.Fatalf("lost receipt (%+v) or contract (%+v)", run.receipt, run.contract)
		}
		if obs := *observed; len(obs) == 0 || obs[len(obs)-1].Invocation.ToolPayloadChunkable {
			t.Fatalf("command truncation observed chunkable payload: %+v", obs)
		}
		chunkRun := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-trunc-write", Name: "write", Args: map[string]any{}, ArgsTruncated: true,
		}, baseToolCtx, nil, 0, "msg-1b", api.CoordinatorRunContext{})
		if chunkRun.reject == nil || chunkRun.reject.Code() != "TOOL_ARGS_TRUNCATED" {
			t.Fatalf("write reject = %+v, want TOOL_ARGS_TRUNCATED", chunkRun.reject)
		}
		obs := *observed
		if len(obs) == 0 || !obs[len(obs)-1].Invocation.ToolPayloadChunkable {
			t.Fatalf("write truncation did not observe a chunkable payload: %+v", obs)
		}
		if len(obs) == 0 || !slices.Contains(obs[len(obs)-1].Invocation.ArgValidationErrors, "TOOL_ARGS_TRUNCATED") || !obs[len(obs)-1].Reach.Known() {
			t.Fatalf("BlockPlane GuardContext missing pre-observation ArgumentValidation or Reach: %+v", obs)
		}
		_ = loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-trunc-2", Name: "command", Args: map[string]any{}, ArgsTruncated: true,
		}, baseToolCtx, nil, 0, "msg-2", api.CoordinatorRunContext{})
		obs = *observed
		if got := obs[len(obs)-1].Counters.SameCodeRejectRun; got < 2 {
			t.Fatalf("OAR same_code_reject_run = %d, want >= 2 through BlockPlane", got)
		}
	})

	t.Run("malformed_preserves_code_and_blockplane_copy", func(t *testing.T) {
		*observed = nil
		run := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-malformed", Name: "command", Args: map[string]any{}, ArgsMalformed: true,
		}, baseToolCtx, nil, 0, "msg-3", api.CoordinatorRunContext{})
		if run.reject == nil || run.reject.Code() != "TOOL_ARGS_MALFORMED" {
			t.Fatalf("reject = %+v, want TOOL_ARGS_MALFORMED", run.reject)
		}
		cause := toolrejection.AsToolReject(run.reject)
		if cause == nil || !cause.ArgumentValidation {
			t.Fatalf("cause = %+v, want ArgumentValidation=true", cause)
		}
		if run.receipt == nil || run.contract.Digest() != commandContract.Digest() {
			t.Fatalf("lost receipt or contract: %+v", run)
		}
		if strings.TrimSpace(run.content) == "" {
			t.Fatal("TOOL_ARGS_MALFORMED delivered no refusal content")
		}
		obs := *observed
		if len(obs) == 0 || !slices.Contains(obs[len(obs)-1].Invocation.ArgValidationErrors, "TOOL_ARGS_MALFORMED") {
			t.Fatalf("BlockPlane GuardContext missing TOOL_ARGS_MALFORMED in ArgValidationErrors: %+v", obs)
		}
	})
}

func assertInvalidAndOwnerFailedCuration(
	t *testing.T,
	loop *PromptLoop,
	sess *api.Session,
	baseToolCtx tools.ToolContext,
	brokenContract toolcontract.Contract,
	observed *[]*oar.GuardContext,
) {
	t.Helper()
	t.Run("invalid_missing_required_and_pruned_unsupported_and_argdiag", func(t *testing.T) {
		*observed = nil
		missingRun := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-missing", Name: "command", Args: map[string]any{},
		}, baseToolCtx, nil, 0, "msg-4", api.CoordinatorRunContext{})
		if missingRun.reject == nil || missingRun.reject.Code() != "TOOL_ARGS_INVALID" {
			t.Fatalf("reject = %+v, want TOOL_ARGS_INVALID", missingRun.reject)
		}
		missingCause := toolrejection.AsToolReject(missingRun.reject)
		if missingCause == nil || !missingCause.ArgumentValidation {
			t.Fatalf("missing cause = %+v", missingCause)
		}
		if missingCause.Data["missing_required_arguments"] == "" || missingCause.Data["missing_required_paths"] == nil || missingCause.Data["schema_issues"] == nil {
			t.Fatalf("lost missing-required diagnostics: %+v", missingCause.Data)
		}
		if missingCause.Data["profile"] != "coordinator" || missingCause.Data["turn_surface"] != "implement_investigate" {
			t.Fatalf("missing occurrence context keys: %+v", missingCause.Data)
		}
		if !strings.Contains(missingRun.content, "command") {
			t.Fatalf("expected missing required field in rendered OAR copy, got %q", missingRun.content)
		}

		prunedRun := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID:   "tc-pruned",
			Name: "command",
			Args: map[string]any{
				"command":            "ls",
				"capability_request": map[string]any{"write_root": "/tmp"},
			},
		}, baseToolCtx, nil, 0, "msg-5", api.CoordinatorRunContext{})
		prunedCause := toolrejection.AsToolReject(prunedRun.reject)
		if prunedCause == nil || prunedRun.reject.Code() != "TOOL_ARGS_INVALID" || !prunedCause.ArgumentValidation {
			t.Fatalf("pruned field reject = %+v, cause = %+v", prunedRun.reject, prunedCause)
		}
		unsupported, _ := prunedCause.Data["unsupported_property_paths"].([]string)
		if len(unsupported) == 0 {
			t.Fatalf("expected unsupported_property_paths on pruned schema rejection: %+v", prunedCause.Data)
		}

		fullCtx := baseToolCtx
		fullCtx.Turn.TurnOfferedToolSchemas = nil
		misplacedRun := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID:   "tc-misplaced",
			Name: "command",
			Args: map[string]any{"command": "ls", "write_root": "/tmp"},
		}, fullCtx, nil, 0, "msg-6", api.CoordinatorRunContext{})
		misplacedCause := toolrejection.AsToolReject(misplacedRun.reject)
		if misplacedCause == nil || misplacedCause.Data["belongs_under"] != "capability_request" {
			t.Fatalf("lost argdiag belongs_under diagnostic: %+v", misplacedCause)
		}
		if !strings.Contains(misplacedRun.content, "capability_request") {
			t.Fatalf("expected argdiag misplaced-field OAR copy, got %q", misplacedRun.content)
		}
	})

	t.Run("owner_failed_preserves_non_arg_validation_and_owner_error_class", func(t *testing.T) {
		*observed = nil
		run := loop.Tools.executeToolCall(t.Context(), sess, sess.ID, "", nil, api.ToolCall{
			ID: "tc-owner", Name: "mcp_broken_schema", Args: map[string]any{"bad": "x"},
		}, baseToolCtx, nil, 0, "msg-7", api.CoordinatorRunContext{})
		if run.reject == nil || run.reject.Code() != toolrejection.ToolOwnerFailedCode {
			t.Fatalf("reject = %+v, want %s", run.reject, toolrejection.ToolOwnerFailedCode)
		}
		cause := toolrejection.AsToolReject(run.reject)
		if cause == nil || cause.ArgumentValidation || cause.FailureClass != api.FailureClassOwnerError {
			t.Fatalf("owner failure cause = %+v, want ArgumentValidation=false and owner_error", cause)
		}
		if run.failure == nil || run.failure.Class != api.FailureClassOwnerError {
			t.Fatalf("run.failure = %+v, want owner_error", run.failure)
		}
		if run.receipt == nil || run.contract.Digest() != brokenContract.Digest() {
			t.Fatalf("lost receipt (%+v) or contract (%+v)", run.receipt, run.contract)
		}
		obs := *observed
		if len(obs) == 0 || len(obs[len(obs)-1].Invocation.ArgValidationErrors) != 0 {
			t.Fatalf("TOOL_OWNER_FAILED must not populate ArgValidationErrors: %+v", obs)
		}
	})
}
