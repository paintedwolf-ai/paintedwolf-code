package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// bundledStandardGate loads bundled rules without overlays.
func bundledStandardGate(t *testing.T) hitl.ApprovalGate {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	contractcheck.FailErr(t, "load bundled approval store", err)
	return settings.NewRuleApprovalGate(store, settings.NoSources())
}

func evalBundled(t *testing.T, action hitl.ProposedAction) *hitl.ApprovalResult {
	t.Helper()
	res, err := bundledStandardGate(t).Evaluate(t.Context(), action)
	contractcheck.FailErr(t, "gate Evaluate", err)
	return res
}

func TestBundledCommandBranchesOnContainmentNotCommandText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contained := hitl.Contained{
		FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{dir},
	}
	for _, command := range []string{
		"go test ./...",
		"cat /etc/hosts",
		`sh -c "cat /etc/hosts"`,
		"cp a.txt /etc/hosts",
		"git push origin main",
	} {
		approved := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": command},
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
			Execution: hitl.ActionExecution{
				Contained: contained,
			},
		})
		if !approved.AutoApproved() || approved.Required() || approved.Denied {
			t.Errorf("contained command %q must run: %+v", command, approved)
		}

		asked := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": command},
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
		})
		if !asked.Required() || asked.Denied {
			t.Errorf("uncontained command %q must ask: %+v", command, asked)
		}
	}
}

func TestBundledContainedFSBlastAutoApproves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{dir}}
	for _, cmd := range []string{
		"rm -rf ./build",
		"dd if=/dev/zero of=disk.img bs=1 count=1",
		"rm --force ./tmp.o",
	} {
		res := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command",
				Args: map[string]any{"command": cmd},
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
			Execution: hitl.ActionExecution{
				Contained: contained,
			},
		})
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("Contained FS command %q must auto-approve: %+v", cmd, res)
		}
	}
}

// Chown path escapes use the write-root approval gate.
func TestBundledStandardChownPathEscapeAsksOutsideRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inProject := evalBundled(t, hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "chown",
			Files: []string{filepath.Join(dir, "run.sh")},
		},
		Scope: hitl.ActionScope{
			ProjectDir: dir,
		},
	})
	if !inProject.AutoApproved() || inProject.Required() {
		t.Fatalf("in-project chown must auto-approve (recoverable): %+v", inProject)
	}
	escaping := evalBundled(t, hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "chown",
			Files: []string{"/etc/passwd"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: dir,
		},
	})
	if !escaping.Required() || escaping.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots at Balanced: %+v", escaping)
	}
}

func TestBundledStandardNativePathEscapeAsksOutsideRoots(t *testing.T) {
	t.Parallel()
	res := evalBundled(t, hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{"/unattached/outside-write.txt"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
		},
	})
	if !res.Required() || res.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots at Balanced: %+v", res)
	}
}

// Installed MCP tools are recoverable at Balanced.
func TestBundledBalancedRunsMCPSilently(t *testing.T) {
	t.Parallel()
	res := evalBundled(t, hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "mcp.example.do_thing",
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
		},
	})
	if !res.AutoApproved() || res.Required() {
		t.Fatalf("MCP tool must run silently at Balanced: %+v", res)
	}
}

// Strict confirms MCP tools until the human selects a host-minted bounded lease.
func TestStrictAsksOnMCPUntilLeased(t *testing.T) {
	t.Parallel()
	global := filepath.Join(t.TempDir(), "approvals.yaml")
	contractcheck.FailErr(t, "write strict global overlay",
		os.WriteFile(global, []byte("approval_posture: strict\nrules:\n"), 0o644))
	store, err := settings.NewApprovalStoreAt(global)
	contractcheck.FailErr(t, "load strict approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "mcp_example_do_thing",
		},
		Resources: hitl.ActionResources{
			ApprovalCategory: string(settings.ApprovalCategoryMCP),
			ApprovalSubject:  "example.do_thing",
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
		},
	}
	res, err := gate.Evaluate(t.Context(), action)
	contractcheck.FailErr(t, "gate Evaluate", err)
	if !res.Required() {
		t.Fatalf("MCP tool must ask at Strict: %+v", res)
	}

	offers := gate.GrantOffers(action, res)
	var chatOffer *hitl.ApprovalGrantOffer
	for i := range offers {
		if offers[i].Scope == hitl.ApprovalGrantScopeChat {
			chatOffer = &offers[i]
			break
		}
	}
	if chatOffer == nil {
		t.Fatalf("missing task lease offer: %+v", offers)
	}
	_, err = gate.ApplyGrant(chatOffer.Grant)
	contractcheck.FailErr(t, "ApplyGrant", err)
	res, err = gate.Evaluate(t.Context(), action)
	contractcheck.FailErr(t, "gate Evaluate after lease", err)
	if !res.AutoApproved() {
		t.Fatalf("leased MCP tool must run silently at Strict: %+v", res)
	}
}

// A retired tool holds no tool tier. Its name keeps a tier only as the native
// operation command reaches, so approvals keep their class after retirement.
func TestRetiredToolsHoldNoToolTier(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "schemas", "agent-public-retirements.json"))
	contractcheck.FailErr(t, "read retirements", err)
	var doc struct {
		Vocabularies struct {
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"vocabularies"`
	}
	contractcheck.FailErr(t, "decode retirements", json.Unmarshal(raw, &doc))
	if len(doc.Vocabularies.Tools) == 0 {
		t.Fatal("no retired tools read")
	}
	for tool, entry := range doc.Vocabularies.Tools {
		if settings.ToolTierDeclared(tool) {
			t.Errorf("retired tool %s holds a tool tier", tool)
		}
		// A successor list names tools; a content contract replaces a tool with no operation.
		var successors []string
		if json.Unmarshal(entry, &successors) == nil && slices.Equal(successors, []string{"command"}) && !settings.OperationTierDeclared(tool) {
			t.Errorf("retired tool %s lost the operation tier command reaches", tool)
		}
	}
}

// TestEveryNativeToolDeclaresATier checks approval classification coverage.
func TestEveryNativeToolDeclaresATier(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateApprovalReversibility(); err != nil {
		t.Fatalf("approval_reversibility map: %v", err)
	}
	manifest := append(cfg.AllTools(), cfg.AllOperations()...)
	if len(manifest) == 0 {
		t.Fatal("native tools manifest is empty")
	}
	var undeclared []string
	for _, tool := range manifest {
		if !settings.ToolTierDeclared(tool) && !settings.OperationTierDeclared(tool) {
			undeclared = append(undeclared, tool)
		}
	}
	if len(undeclared) > 0 {
		t.Fatalf("native tools missing approval reversibility (add approval_reversibility.<id> on "+
			"pack platform/tools/native-tools.yaml and run ./task codegen:native-tool-contracts): %s",
			strings.Join(undeclared, ", "))
	}
}

func TestClassifyTierNativeScanDrillDownReversible(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"scan_list", "scan_summary", "scan_query", "scan_compare"} {
		tier := settings.ClassifyTier(hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: tool,
			},
			Scope: hitl.ActionScope{
				ProjectDir: "/proj",
			},
		})
		if tier != settings.TierReversible {
			t.Fatalf("%s: got tier %v want reversible", tool, tier)
		}
	}
}

func TestBundledStandardRunsNativeScanDrillDownSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tool := range []string{"scan_list", "scan_summary", "scan_query", "scan_compare"} {
		res := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: tool,
			},
			Scope: hitl.ActionScope{
				ProjectDir: dir,
			},
		})
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("native scan drill-down %q must auto-approve under Standard: %+v", tool, res)
		}
	}
}

func TestBundledStandardRunsContainedEditsAndDeletesSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, action := range []hitl.ProposedAction{
		{Invocation: hitl.ActionInvocation{Tool: "write", Files: []string{filepath.Join(dir, "a.go")}}, Scope: hitl.ActionScope{ProjectDir: dir}},
		{Invocation: hitl.ActionInvocation{Tool: "edit", Files: []string{filepath.Join(dir, "b.go")}}, Scope: hitl.ActionScope{ProjectDir: dir}},
		{Invocation: hitl.ActionInvocation{Tool: "delete", Files: []string{filepath.Join(dir, "c.go")}}, Scope: hitl.ActionScope{ProjectDir: dir}},
	} {
		res := evalBundled(t, action)
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("contained %s must auto-approve under Standard: %+v", action.Invocation.Tool, res)
		}
	}
}

func TestBundledApprovalsNoAskOnChmod(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "host", "approvals.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	body := string(data)
	if strings.Contains(body, "pattern: chmod") {
		t.Fatal("bundled approvals.yaml must not ask on chmod by default")
	}
}

func TestBundledApprovalsNoAskOnFileOps(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "security", "host", "approvals.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	body := string(data)
	for _, tool := range []string{"copy", "move", "mkdir"} {
		if strings.Contains(body, "pattern: "+tool) {
			t.Fatalf("bundled approvals.yaml must not ask on %s by default", tool)
		}
	}
}

func TestCheckpointRoutesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	want := []string{
		"GET /v1/sessions/{id}/checkpoints",
		"POST /v1/sessions/{id}/checkpoints/{checkpoint_id}",
	}
	for _, w := range want {
		ok := false
		for _, r := range routes {
			if r.Method+" "+r.Path == w {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("missing route %s", w)
		}
	}
}

func TestCheckpointRoutesNotMarkedStub(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read file", err)
	body := string(data)
	for _, fragment := range []string{
		"/v1/sessions/{id}/checkpoints:",
		"/v1/sessions/{id}/checkpoints/{checkpoint_id}:",
	} {
		idx := strings.Index(body, fragment)
		if idx < 0 {
			t.Fatalf("missing OpenAPI path %s", fragment)
		}
		window := body[idx : idx+400]
		if strings.Contains(window, "x-paintedwolf-status: stub") {
			t.Fatalf("checkpoint route %s must not be x-paintedwolf-status: stub", fragment)
		}
	}
}

func TestApprovalDecisionActionEnumSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	goEnums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums in pkg/api", err)
	if !strings.Contains(strings.Join(goEnums["ApprovalDecisionAction"], ","), "approve") {
		t.Fatalf("ApprovalDecisionAction = %v", goEnums["ApprovalDecisionAction"])
	}
}

// denyingHITL captures the raised card and rejects every approval.
type denyingHITL struct {
	request  hitl.CheckpointRequest
	guidance string
}

func (m *denyingHITL) RequestCheckpoint(_ context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.request = request
	return &hitl.CheckpointResponse{CheckpointID: "chk-denied", Status: hitl.DecisionStatusPending}, nil
}

func (m *denyingHITL) PollCheckpoint(_ context.Context, checkpointID string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: checkpointID,
		Status:       hitl.DecisionStatusRejected,
		Result:       &hitl.DecisionResult{Approved: false, Comments: m.guidance},
	}, nil
}

func (m *denyingHITL) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (m *denyingHITL) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *denyingHITL) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (m *denyingHITL) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *denyingHITL) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *denyingHITL) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (m *denyingHITL) PatchPendingToolApprovalAIRationale(context.Context, string, string) error {
	return nil
}
func (m *denyingHITL) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (m *denyingHITL) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

// outcomeRenderer adapts the bundled approval-outcome catalog to the executor.
type outcomeRenderer struct {
	cat *approvaloutcome.Catalog
}

func (o outcomeRenderer) ApprovalOutcome(code string, ctx map[string]any) string {
	return o.cat.Message(code, ctx)
}

func loadOutcomeRenderer(t *testing.T) outcomeRenderer {
	t.Helper()
	cfg, err := approvaloutcome.Load()
	contractcheck.FailErr(t, "load approval-outcome catalog", err)
	return outcomeRenderer{cat: approvaloutcome.NewCatalog(cfg)}
}

// outsideReadProfiles loads bundled tool profiles and lets the implement
// profile read, mirroring the shipped native read path.
func outsideReadProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for i := range profiles {
		if profiles[i].ID == "implement" || profiles[i].ID == "coordinator" {
			if profiles[i].Tools == nil {
				profiles[i].Tools = map[string]bool{}
			}
			profiles[i].Tools["read"] = true
		}
	}
	return profiles
}

// denyOutsideRead runs a native read of path whose approval the human denies.
// It returns the model-visible error text, the structured refusal, and the
// compiled card the human saw.
func denyOutsideRead(t *testing.T, path string) (string, *guidance.Refusal, *hitl.ApprovalPlan) {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	contractcheck.FailErr(t, "settings.NewApprovalStoreAt", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, outsideReadProfiles(t))
	mgr := &denyingHITL{guidance: "Use the read tool and summarize the current file instead."}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	reg := tools.NewDefaultRegistry()
	if err := reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "ok", nil
	}); err != nil {
		contractcheck.FailErr(t, "register read", err)
	}
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	exec.Approvals.SetCheckpointManager(t.Context(), mgr, gate)
	exec.Approvals.SetApprovalOutcomeRenderer(loadOutcomeRenderer(t))

	root := t.TempDir()
	_, err = exec.Invoke(context.Background(), "read", map[string]any{"path": path}, tools.ToolContext{
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "r1",
		},
		Identity: tools.InvocationIdentity{SessionID: "sess-1", Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected the denied approval to fail the invocation")
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("expected an approval refusal, got %v", err)
	}
	plan, planErr := hitl.CompileCheckpointApprovalPlan(mgr.request)
	contractcheck.FailErr(t, "compile captured approval plan", planErr)
	return err.Error(), refusal, plan
}

// TestDeniedMissingOutsidePathMatchesExistingPathByteForByte proves the model
// cannot probe existence through denial: a denied read of a missing outside
// path and of an existing one leave byte-identical model-visible transcripts,
// while the human cards differ.
func TestDeniedMissingOutsidePathMatchesExistingPathByteForByte(t *testing.T) {
	t.Parallel()
	// The fixture lives under the user home: a path under the OS temp dir is
	// ordinary scratch access and would never reach the outside-root gate.
	home, err := os.UserHomeDir()
	contractcheck.FailErr(t, "resolve user home", err)
	base, err := os.MkdirTemp(home, "wr11-contract-fixture-")
	contractcheck.FailErr(t, "create outside fixture folder", err)
	t.Cleanup(func() { os.RemoveAll(base) })
	existing := filepath.Join(base, "release.go")
	contractcheck.FailErr(t, "create existing target", os.WriteFile(existing, []byte("contents"), 0o644))
	missing := filepath.Join(base, "missing.go")

	existingText, existingRefusal, existingPlan := denyOutsideRead(t, existing)
	missingText, missingRefusal, missingPlan := denyOutsideRead(t, missing)

	type transcript struct {
		Body         string
		Outcome      api.ToolResultOutcome
		Codes        []string
		Feedback     []api.ToolFeedback
		RejectCode   string
		Observation  string
		Data         map[string]any
		FailureClass string
		Retryable    bool
		OwnerRef     string
		ErrorText    string
	}
	render := func(text string, refusal *guidance.Refusal) transcript {
		reject := toolrejection.AsToolReject(refusal)
		if reject == nil {
			t.Fatalf("refusal lost its structured cause: %q", text)
		}
		return transcript{
			Body: refusal.Body, Outcome: refusal.Facts.Outcome, Codes: refusal.Facts.Codes,
			Feedback: refusal.Facts.Feedback, RejectCode: reject.Code, Observation: reject.Observation,
			Data: reject.Data, FailureClass: reject.FailureClass, Retryable: reject.Retryable,
			OwnerRef: reject.OwnerRef, ErrorText: text,
		}
	}
	existingBytes, err := json.Marshal(render(existingText, existingRefusal))
	contractcheck.FailErr(t, "marshal existing-path transcript", err)
	missingBytes, err := json.Marshal(render(missingText, missingRefusal))
	contractcheck.FailErr(t, "marshal missing-path transcript", err)
	if !bytes.Equal(existingBytes, missingBytes) {
		t.Fatalf("denied transcripts differ between a missing and an existing outside path:\nexisting: %s\nmissing:  %s", existingBytes, missingBytes)
	}
	for _, leaked := range []string{"doesn't exist", "does not exist", "file.exists"} {
		if strings.Contains(missingText, leaked) {
			t.Fatalf("denial transcript leaked the existence fact %q: %q", leaked, missingText)
		}
	}

	// The human cards differ: the missing path leads with its absence and names
	// the resolved spelling; the existing path leads with nothing.
	if missingPlan.Presentation.Lead != "This path doesn't exist" {
		t.Fatalf("missing-path card lead = %q, want the missing-path fact", missingPlan.Presentation.Lead)
	}
	if existingPlan.Presentation.Lead == "This path doesn't exist" {
		t.Fatalf("existing-path card must not lead with the missing-path fact: %q", existingPlan.Presentation.Lead)
	}
	cited := func(plan *hitl.ApprovalPlan, key string) string {
		for _, fact := range plan.Presentation.Cited {
			if fact.Key == key {
				return fact.Value
			}
		}
		return ""
	}
	if cited(missingPlan, "file.exists") != "this path doesn't exist" {
		t.Fatalf("missing-path card does not cite file.exists: %+v", missingPlan.Presentation.Cited)
	}
	if cited(existingPlan, "file.exists") != "" {
		t.Fatalf("existing-path card cites file.exists: %+v", existingPlan.Presentation.Cited)
	}
	if want := fspath.CanonicalPath(missing); cited(missingPlan, "file.path") != want {
		t.Fatalf("missing-path card names %q, want the resolved spelling %q", cited(missingPlan, "file.path"), want)
	}

	// The rationale request learns nothing about existence: the host-authored
	// explanation carries no missing-path copy.
	for _, part := range []string{
		missingPlan.Presentation.Impact, missingPlan.Presentation.Who, missingPlan.Presentation.IfWrong,
	} {
		for _, leaked := range []string{"doesn't exist", "does not exist", "file.exists"} {
			if strings.Contains(part, leaked) {
				t.Fatalf("approval explanation leaked the existence fact %q: %q", leaked, part)
			}
		}
	}
}
