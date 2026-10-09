package contract

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Project files a trust surface loads ask through agent policy; only the
// host's own state tree denies, and files no loader reads stay ordinary.
func TestProjectAgentPolicyAsksWhileHostStateDenies(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Join(proj, settingsoverlay.DirName()), 0o700))
	gate := approvalGateForFloor(t)

	for _, rel := range []string{
		settingsoverlay.Rel(settingsoverlay.BasenameApprovals),
		settingsoverlay.Rel(settingsoverlay.BasenameMCP),
		settingsoverlay.Rel("workflows", "review", "workflow.yaml"),
		"AGENTS.md",
	} {
		path := filepath.Join(proj, filepath.FromSlash(rel))
		target, ok := hitl.AgentPolicyTargetFor(path, proj)
		if !ok {
			t.Fatalf("%s is not agent policy", rel)
		}
		res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
			Tool: "write", Files: []string{path}, ProjectDir: proj, AgentPolicy: []hitl.AgentPolicyTarget{target},
		})
		testutil.FailErr(t, "Evaluate "+rel, err)
		if res == nil || res.Denied || !res.Required() || !slices.Contains(res.Decision.Gates(), api.GateAgentPolicyChange) {
			t.Fatalf("%s: want an agent-policy ask, got %+v", rel, res)
		}
	}

	hostSink := filepath.Join(cfg, "mcp.yaml")
	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
		Tool: "write", Files: []string{hostSink}, ProjectDir: proj,
	})
	testutil.FailErr(t, "Evaluate host sink", err)
	if res == nil || !res.Denied || res.DenyCode != isolation.CodeControlPlaneDenied {
		t.Fatalf("host state want %s, got %+v", isolation.CodeControlPlaneDenied, res)
	}

	for _, free := range []string{
		filepath.Join(proj, settingsoverlay.DirName(), "blueprints", "a.yaml"),
		filepath.Join(cfg, "drafts", "x.txt"),
		filepath.Join(proj, "lycaon", "config", "packs", "painted-wolf", "security", "host", "approvals.yaml"),
	} {
		if _, ok := hitl.AgentPolicyTargetFor(free, proj); ok {
			t.Errorf("%s classified as agent policy; no loader reads it", free)
		}
		res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
			Tool: "write", Files: []string{free}, ProjectDir: proj,
		})
		testutil.FailErr(t, "Evaluate "+free, err)
		if res != nil && res.Denied && res.DenyCode != isolation.CodeControlPlaneDenied {
			t.Errorf("%s denied with %s", free, res.DenyCode)
		}
	}
}

// The resolver never refuses agent policy; the approval gate decides.
func TestAgentPolicyResolvesForReview(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Join(proj, settingsoverlay.DirName()), 0o700))
	tctx := tools.ToolContext{
		Roots: []projectroot.RootRef{{ID: "primary", Path: proj, IsPrimary: true}},
	}
	for _, rel := range []string{settingsoverlay.Rel(settingsoverlay.BasenameApprovals), ".env", "docs/AGENTS.md"} {
		if _, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, rel); err != nil {
			t.Errorf("ResolveWrite(%s) refused: %v", rel, err)
		}
	}
}

func TestHardDenyWriteSpecsConsumedByBuildProfile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	specs, err := confine.HardDenyWriteSpecs(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "HardDenyWriteSpecs", err)
	if len(specs) == 0 {
		t.Fatal("HardDenyWriteSpecs empty")
	}
	profile, err := confine.BuildProfile(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "BuildProfile", err)
	// Match the sink within the write-deny block.
	sinkAt := strings.Index(profile, settingsoverlay.BasenameApprovals)
	if sinkAt < 0 {
		t.Fatalf("profile missing approvals sink:\n%s", profile)
	}
	denyAt := strings.LastIndex(profile[:sinkAt], "(deny file-write*")
	if denyAt < 0 {
		t.Fatalf("profile missing deny file-write*:\n%s", profile)
	}
	denyWrite := profile[denyAt:]
	if end := strings.Index(denyWrite, ")\n("); end >= 0 {
		denyWrite = denyWrite[:end]
	}
	foundPerm := false
	for _, s := range specs {
		if strings.Contains(s.Literal, settingsoverlay.BasenameApprovals) {
			foundPerm = true
			if !strings.Contains(denyWrite, settingsoverlay.BasenameApprovals) {
				t.Fatalf("write hard-deny missing approvals.yaml:\n%s", denyWrite)
			}
		}
	}
	if !foundPerm {
		t.Fatalf("specs missing approvals.yaml: %+v", specs)
	}
}

func TestSettingsOverlayBasenamesPathHelpersRegistered(t *testing.T) {
	t.Parallel()
	registered := map[string]bool{}
	for _, b := range settingsoverlay.SettingsOverlayBasenames() {
		registered[b] = true
	}
	root := contractcheck.RepoRoot(t)
	pkgs := []string{
		filepath.Join(root, "lycaon/internal/settings"),
		filepath.Join(root, "lycaon/internal/mcp"),
		filepath.Join(root, "lycaon/internal/llm"),
		filepath.Join(root, "lycaon/internal/standingpatterns"),
	}
	fset := token.NewFileSet()
	for _, dir := range pkgs {
		entries, err := os.ReadDir(dir)
		testutil.FailErr(t, "ReadDir "+dir, err)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(fset, path, nil, 0)
			testutil.FailErr(t, "Parse "+path, err)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Join" {
					return true
				}
				if len(call.Args) < 3 {
					return true
				}
				mid, ok := call.Args[len(call.Args)-2].(*ast.BasicLit)
				if !ok || mid.Kind != token.STRING || strings.Trim(mid.Value, `"`) != settingsoverlay.DirName() {
					return true
				}
				last, ok := call.Args[len(call.Args)-1].(*ast.BasicLit)
				if !ok || last.Kind != token.STRING {
					return true
				}
				base := strings.Trim(last.Value, `"`)
				if strings.Contains(base, "/") {
					return true
				}
				if !registered[base] {
					t.Errorf("%s: Join(.paintedwolf, %q) not in SettingsOverlayBasenames", path, base)
				}
				return true
			})
		}
	}
}

func TestHardDenyWriteSpecsNoBasenameForkInBackends(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "lycaon/internal/confine/confine.go"))
	testutil.FailErr(t, "read confine.go", err)
	if !strings.Contains(string(src), "HardDenyWriteSpecs(") {
		t.Fatal("BuildProfile must render HardDenyWriteSpecs")
	}
	if strings.Contains(string(src), "approvals.yaml") {
		t.Fatal("confine.go must not hardcode approvals.yaml — use HardDenyWriteSpecs")
	}
}

// Path-mutating tools come from the catalog's declared axis.
func TestPathMutatingToolsComeFromTheCatalog(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)

	catalog := cfg.PathMutatingTools()
	if len(catalog) == 0 {
		t.Fatal("mutates_path axis is empty")
	}
	for _, tool := range catalog {
		if !settings.IsPathMutatingTool(tool) {
			t.Errorf("catalog names %q on mutates_path but the approval gate calls it read-only", tool)
		}
	}

	// Read-only tools keep the opposite classification.
	for _, tool := range []string{"read", "grep", "find", "stat", "wc", "list_dir", "diff"} {
		if settings.IsPathMutatingTool(tool) {
			t.Errorf("%q reads only but the approval gate calls it a write crossing", tool)
		}
	}
}

// [OAR-EVAL-1] The repository-metadata refusal and its Git route survive new mutating tools.
func TestRepositoryMetadataPolicyCoversMutatingTools(t *testing.T) {
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	pipeline := stockRejectionPipeline(t)
	for _, name := range append(cfg.PathMutatingTools(), "future_write_tool") {
		assertIntrinsicRejection(t, pipeline, name, "GIT_INTERNALS_WRITE_DENIED")
	}
}

// chmod and chown mutate through the same write door as write and delete.
func TestMetadataMutationsAreWriteCrossings(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"chmod", "chown"} {
		if !settings.IsPathMutatingTool(tool) {
			t.Errorf("%s changes a file on disk and must classify as a write crossing", tool)
		}
	}
}

func approvalGateForFloor(t *testing.T) hitl.ApprovalGate {
	t.Helper()
	tmp := t.TempDir()
	cfg := struct {
		Rules []settings.ApprovalRule `yaml:"rules"`
	}{Rules: nil}
	data, err := yaml.Marshal(cfg)
	testutil.FailErr(t, "marshal", err)
	// Stage bundled rules while preserving a real device overlay path.
	configtest.Overlay(t, map[config.Rel]string{config.SecurityApprovals: string(data)})
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return settings.NewRuleApprovalGate(store, settings.NoSources())
}

func asToolReject(err error, out **toolrejection.ToolReject) bool {
	if err == nil {
		return false
	}
	r := &toolrejection.ToolReject{}
	if errors.As(err, &r) {
		*out = r
		return true
	}
	return false
}
