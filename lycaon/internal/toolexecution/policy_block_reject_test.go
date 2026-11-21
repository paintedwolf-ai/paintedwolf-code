package toolexecution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// A profile denial leaves Invoke as a typed reject, so the ledger records
// policy_rejection. An untyped error settles as TOOL_OWNER_FAILED/owner_error,
// which no repeat counter charges and which ends a worker child.
func TestProfileDenialLeavesInvokeAsTypedReject(t *testing.T) {
	boundary := fixtureBoundary(t)
	reg := tools.NewDefaultRegistry()
	ran := func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "ran", nil }
	if err := reg.Register("write", ran); err != nil {
		t.Fatalf("register write: %v", err)
	}
	exec := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Identity.Agent = "no-such-profile"
	_, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path": "main.go", "content": "x",
	}, tctx)
	if err == nil {
		t.Fatal("a denied profile ran the tool")
	}
	refusal, rendered := guidance.RefusalFromError(err)
	if !rendered {
		t.Fatalf("profile denial did not leave the executor as a rendered refusal: %#v", err)
	}
	if refusal.Code() == "" {
		t.Fatalf("rendered refusal carries no Code: %q", refusal.Body)
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		t.Fatal("rendered refusal lost the reject that caused it")
	}
	if reject.FailureClass != "policy_rejection" {
		t.Fatalf("failure class = %q, want policy_rejection", reject.FailureClass)
	}
}

// A runtime exclusion states its own code.
func TestRuntimeToolDenyStatesItsCode(t *testing.T) {
	boundary := fixtureBoundary(t)
	profile := toolprofiles.NewProfilePolicyEngine(boundary)
	profile.SetRuntimeToolDeny(func(name string) bool { return name == "web_search" })

	decision, err := profile.Evaluate(context.Background(), platform.PolicyContext{
		ToolName: "web_search", ProfileID: "implement",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !decision.Blocked {
		t.Fatal("runtime-denied tool was not blocked")
	}
	if decision.RejectCode != "WEB_SEARCH_DISABLED" {
		t.Fatalf("reject code = %q, want WEB_SEARCH_DISABLED", decision.RejectCode)
	}
	if decision.BlockReason != "WEB_SEARCH_DISABLED" {
		t.Fatalf("block reason = %q, want WEB_SEARCH_DISABLED", decision.BlockReason)
	}
}

// The executor's last exit renders a reject that reached it still typed.
func TestUnrenderedRejectIsRenderedAtTheBoundary(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	bare := &toolrejection.ToolReject{Code: "SANDBOX_SOCKS_PROXY_INVALID", Data: map[string]any{
		"reason": "socks_proxy must be a boolean",
	}}

	out := exec.Rejections.renderUnrenderedReject("command", bare)
	refusal, rendered := guidance.RefusalFromError(out)
	if !rendered {
		t.Fatalf("bare reject left the boundary unrendered: %#v", out)
	}
	if refusal.Code() != bare.Code {
		t.Fatalf("rendered code = %q, want %q", refusal.Code(), bare.Code)
	}
	if !strings.Contains(refusal.Body, "socks_proxy must be a boolean") {
		t.Fatalf("rendered body dropped the cause: %q", refusal.Body)
	}
	if toolrejection.AsToolReject(out) == nil {
		t.Fatal("rendering lost the reject that caused it")
	}
}

// The backstop renders refusals; it does not invent them.
func TestBoundaryLeavesPlainErrorsAlone(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	if got := exec.Rejections.renderUnrenderedReject("command", nil); got != nil {
		t.Fatalf("nil error became %#v", got)
	}
	plain := context.Canceled
	if got := exec.Rejections.renderUnrenderedReject("command", plain); !errors.Is(got, plain) {
		t.Fatalf("plain error rewritten to %#v", got)
	}
}

func TestExecutorPreservesHandlerCancellationAndPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(ctx context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		cancel()
		return "partial output", ctx.Err()
	}); err != nil {
		t.Fatalf("register read: %v", err)
	}
	executor := NewExecutor(nil, reg, "implement")
	output, err := executor.Invoke(ctx, "read", map[string]any{"path": "fixture.txt"}, fixtureToolContext(t.TempDir()))
	if !errors.Is(err, context.Canceled) || toolrejection.AsToolReject(err) != nil {
		t.Fatalf("cancellation became owner rejection: %v", err)
	}
	if output != "partial output" {
		t.Fatalf("partial output = %q", output)
	}
}

func TestExecutorPreservesStructuredFailurePartialOutput(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	const partial = `{"changed_paths":["retained.txt"],"completed":false}`
	cause := &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, Data: map[string]any{"attempted": true}}
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) { return partial, cause }); err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	executor := NewExecutor(nil, reg, "implement")
	output, err := executor.Invoke(t.Context(), "read", map[string]any{"path": "fixture.txt"}, fixtureToolContext(t.TempDir()))
	reject := toolrejection.AsToolReject(err)
	if output != partial || reject == nil || reject.Code != cause.Code {
		t.Fatalf("partial outcome or failure identity lost: %q %v", output, err)
	}
}

func TestCoordinatorProfileDenialDoesNotInventDispatchState(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "coordinator", Tools: map[string]bool{"read": true}}})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	for _, name := range []string{"state_update", "write"} {
		decision, err := policy.Evaluate(t.Context(), platform.PolicyContext{ToolName: name, ProfileID: "coordinator"})
		if err != nil {
			t.Fatalf("evaluate %s: %v", name, err)
		}
		if !decision.Blocked || decision.RejectCode != "COORDINATOR_TOOL_DENIED" {
			t.Fatalf("profile denial invented workflow state: %+v", decision)
		}
	}
}

func TestExecutorCapabilityParseRejectIncludesContractFields(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ran", nil
	}); err != nil {
		t.Fatalf("register command: %v", err)
	}
	executor := NewExecutor(nil, reg, "implement")
	_, err := executor.Invoke(t.Context(), "command", map[string]any{
		"command":            "python3 -c pass",
		"capability_request": map[string]any{"write_root": "receipt.json"},
	}, fixtureToolContext(t.TempDir()))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
		t.Fatalf("err = %v, want SANDBOX_CAPABILITY_REQUEST_INVALID", err)
	}
	fields, _ := reject.Data["capability_request_fields"].([]string)
	if len(fields) == 0 {
		t.Fatalf("capability_request_fields = %#v, want command contract fields", reject.Data["capability_request_fields"])
	}
}

func TestAskExecutorAdmitsOnlyRegisteredReadTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		mcpRead    bool
		allowed    bool
	}{
		{name: "command", registered: true, allowed: true},
		{name: "command_output", registered: true, allowed: true},
		{name: "command_stop", registered: true, allowed: true},
		{name: "verify", registered: true},
		{name: "write", registered: true},
		{name: "mcp_fixture_read", registered: true, mcpRead: true, allowed: true},
		{name: "mcp_fixture_mutation", registered: true},
		{name: "mcp_fixture_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			calls := 0
			handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				calls++
				return "observation", nil
			}
			if tc.registered {
				meta := tools.ToolMeta{Name: tc.name, ReadOnlyHint: tc.mcpRead, ArgsSchema: map[string]any{"type": "object"}}
				if meta.IsMCP() {
					meta.Source, meta.SourceID = tools.ToolSourceMCP, "fixture"
					testutil.FailErr(t, "register MCP tool", reg.RegisterDefinition(tools.Definition{
						Meta: meta, Contract: toolcontract.External("mcp:fixture"), Handler: handler,
					}))
				} else {
					testutil.FailErr(t, "register native tool", reg.Register(tc.name, handler))
				}
			}
			executor := NewExecutor(nil, reg, "implement")
			executor.SetProgramEntry("command", func(args map[string]any) bool {
				_, terminal := args["terminal_capture"]
				_, snapshot := args["snapshot_capture"]
				return terminal || snapshot
			})
			ctx := fixtureToolContext(t.TempDir())
			ctx.Source.AskMode, ctx.Source.ReadOnly = true, true

			out, err := executor.Invoke(t.Context(), tc.name, map[string]any{"command": "git status"}, ctx)
			if tc.allowed {
				testutil.FailErr(t, "run read tool", err)
				if calls != 1 || out != "observation" {
					t.Fatalf("read tool calls = %d, output = %q", calls, out)
				}
				return
			}
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != tools.AskModeActiveCode || calls != 0 || out != "" {
				t.Fatalf("denied tool calls = %d, output = %q, error = %v", calls, out, err)
			}
		})
	}
}

type askWorkerCoordSpy struct {
	ensured int
}

func (s *askWorkerCoordSpy) BeforeWorkerWrite(context.Context, tools.ToolContext, string) error {
	return nil
}

func (s *askWorkerCoordSpy) AfterWorkerWrite(context.Context, tools.ToolContext, string) {}

func (s *askWorkerCoordSpy) ReleaseReservations(context.Context, string, string) error { return nil }

func (s *askWorkerCoordSpy) EnsureBranch(_ context.Context, tctx tools.ToolContext) (tools.ToolContext, error) {
	s.ensured++
	return tctx, nil
}

func TestAskExecutorRejectsPrunedCommandFieldsBeforeAdmission(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(hints)

	for _, c := range []struct {
		name  string
		field string
		value any
	}{
		{name: "capability_request", field: "capability_request", value: map[string]any{"write_root": "/tmp/out"}},
		{name: "secret_use", field: "secret_use", value: map[string]any{"services": []any{"https://api.example.com"}}},
		{name: "snapshot_capture", field: "snapshot_capture", value: map[string]any{"caption": "snap"}},
		{name: "socks_proxy", field: "socks_proxy", value: false},
		{name: "terminal_capture", field: "terminal_capture", value: map[string]any{"winsize": map[string]any{"cols": 80, "rows": 24}}},
		{name: "verification_false", field: "verification", value: false},
		{name: "verification_true", field: "verification", value: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			reg, err := tools.NewCatalogRegistry(cfg)
			testutil.FailErr(t, "catalog registry", err)
			handlerCalls := 0
			testutil.FailErr(t, "register command", reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				handlerCalls++
				return "ran", nil
			}))
			executor := NewExecutor(nil, reg, "implement")
			executor.Rejections.SetRejectFormatter(formatter)
			executor.SetProgramEntry("command", func(args map[string]any) bool {
				_, terminal := args["terminal_capture"]
				_, snapshot := args["snapshot_capture"]
				return terminal || snapshot
			})

			credIndex := &unavailableCredentialIndex{failure: errors.New("credential admission must not run")}
			workerCoord := &askWorkerCoordSpy{}
			tctx := fixtureToolContext(t.TempDir())
			tctx.Identity.ProjectID = "project-1"
			tctx.Identity.WorkerJobID = "worker-job-1"
			tctx.Source.WorkerBranchRoot = ""
			tctx.Source.WorkerCoord = workerCoord
			tctx.Effects.CredentialFiles = credIndex
			tctx.Source.AskMode, tctx.Source.ReadOnly = true, true

			args := map[string]any{
				"command": "pwd",
				c.field:   c.value,
			}
			out, invokeErr := executor.Invoke(t.Context(), "command", args, tctx)
			if invokeErr == nil || out != "" {
				t.Fatalf("expected Ask pruned-field rejection, got out=%q err=%v", out, invokeErr)
			}
			if credIndex.project != "" || workerCoord.ensured != 0 || handlerCalls != 0 {
				t.Fatalf("reached admission or handler: credProject=%q workerEnsured=%d handlerCalls=%d", credIndex.project, workerCoord.ensured, handlerCalls)
			}
			refusal, rendered := guidance.RefusalFromError(invokeErr)
			if !rendered || refusal.Code() != tools.AskModeActiveCode || strings.TrimSpace(refusal.Body) == "" {
				t.Fatalf("refusal rendering failed: rendered=%t code=%q body=%q", rendered, refusal.Code(), refusal.Body)
			}
			reject := toolrejection.AsToolReject(invokeErr)
			if reject == nil || reject.Code != tools.AskModeActiveCode {
				t.Fatalf("reject = %+v, want %s", reject, tools.AskModeActiveCode)
			}
			if reject.Data["tool"] != "command" || reject.Data["field"] != c.field || len(reject.Data) != 2 {
				t.Fatalf("reject.Data = %#v, want tool=command and field=%s only", reject.Data, c.field)
			}
		})
	}
}
