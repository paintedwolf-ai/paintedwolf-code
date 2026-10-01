package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/settings"
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
			Tool: "command", Args: map[string]any{"command": command}, ProjectDir: dir,
			Contained: contained,
		})
		if !approved.AutoApproved() || approved.Required() || approved.Denied {
			t.Errorf("contained command %q must run: %+v", command, approved)
		}

		asked := evalBundled(t, hitl.ProposedAction{
			Tool: "command", Args: map[string]any{"command": command}, ProjectDir: dir,
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
			Tool:       "command",
			Args:       map[string]any{"command": cmd},
			ProjectDir: dir,
			Contained:  contained,
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
		Tool: "chown", Files: []string{filepath.Join(dir, "run.sh")}, ProjectDir: dir,
	})
	if !inProject.AutoApproved() || inProject.Required() {
		t.Fatalf("in-project chown must auto-approve (recoverable): %+v", inProject)
	}
	escaping := evalBundled(t, hitl.ProposedAction{
		Tool: "chown", Files: []string{"/etc/passwd"}, ProjectDir: dir,
	})
	if !escaping.Required() || escaping.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots at Balanced: %+v", escaping)
	}
}

func TestBundledStandardNativePathEscapeAsksOutsideRoots(t *testing.T) {
	t.Parallel()
	res := evalBundled(t, hitl.ProposedAction{
		Tool:       "write",
		Files:      []string{"/unattached/outside-write.txt"},
		ProjectDir: t.TempDir(),
	})
	if !res.Required() || res.Gate() != api.GateOutsideRootsWrite {
		t.Fatalf("native path escape must ask outside_roots at Balanced: %+v", res)
	}
}

// Installed MCP tools are recoverable at Balanced.
func TestBundledBalancedRunsMCPSilently(t *testing.T) {
	t.Parallel()
	res := evalBundled(t, hitl.ProposedAction{Tool: "mcp.example.do_thing", ProjectDir: t.TempDir()})
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
		Tool: "mcp_example_do_thing", ApprovalCategory: string(settings.ApprovalCategoryMCP), ApprovalSubject: "example.do_thing",
		ProjectDir: t.TempDir(), SessionID: "chat-1",
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

// TestEveryNativeToolDeclaresATier checks approval classification coverage.
func TestEveryNativeToolDeclaresATier(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	if err := cfg.ValidateApprovalReversibility(); err != nil {
		t.Fatalf("approval_reversibility map: %v", err)
	}
	manifest := cfg.AllTools()
	if len(manifest) == 0 {
		t.Fatal("native tools manifest is empty")
	}
	var undeclared []string
	for _, tool := range manifest {
		if !settings.ToolTierDeclared(tool) {
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
		tier := settings.ClassifyTier(hitl.ProposedAction{Tool: tool, ProjectDir: "/proj"})
		if tier != settings.TierReversible {
			t.Fatalf("%s: got tier %v want reversible", tool, tier)
		}
	}
}

func TestBundledStandardRunsNativeScanDrillDownSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tool := range []string{"scan_list", "scan_summary", "scan_query", "scan_compare"} {
		res := evalBundled(t, hitl.ProposedAction{Tool: tool, ProjectDir: dir})
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("native scan drill-down %q must auto-approve under Standard: %+v", tool, res)
		}
	}
}

func TestBundledStandardRunsContainedEditsAndDeletesSilently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, action := range []hitl.ProposedAction{
		{Tool: "write", Files: []string{filepath.Join(dir, "a.go")}, ProjectDir: dir},
		{Tool: "edit", Files: []string{filepath.Join(dir, "b.go")}, ProjectDir: dir},
		{Tool: "delete", Files: []string{filepath.Join(dir, "c.go")}, ProjectDir: dir},
	} {
		res := evalBundled(t, action)
		if !res.AutoApproved() || res.Required() {
			t.Fatalf("contained %s must auto-approve under Standard: %+v", action.Tool, res)
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
