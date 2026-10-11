package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveSessionOverridesBundled(t *testing.T) {
	store := workflowdrafts.NewMemory()
	ctx := context.Background()

	sessionYAML := `id: plan
version: 1.0.0
name: session-plan
request:
  question: What should we plan?
phases:
  - id: intake
    activity_label: Test phase
    complete_when: plan_stub_valid
    next: done
  - id: done
    activity_label: Test phase
    terminal: true
`
	if err := store.Upsert(ctx, "sess-1", []byte(sessionYAML), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}

	resolver := Resolver{SessionStore: store, ProjectTierApplies: func(context.Context, string) bool { return true }}
	reg, scopes, err := resolver.Resolve(ctx, "", "sess-1")
	testutil.FailErr(t, "resolver.Resolve failed", err)
	key := workflowdef.ManifestKey("plan", "1.0.0")
	if scopes[key] != string(api.WorkflowScopeSession) {
		t.Fatalf("scope = %q want session", scopes[key])
	}
	m, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "reg.Get failed", err)
	if m.Name != "session-plan" {
		t.Fatalf("name = %q want session-plan", m.Name)
	}
	if len(m.Phases) != 2 || m.Phases[0] != "intake" || m.Phases[1] != "done" {
		t.Fatalf("phases = %v want [intake done]", m.Phases)
	}
}

func TestResolveProjectOverridesBundledSessionWins(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "plan")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	projectYAML := `id: plan
version: 1.0.0
name: project-plan
trigger: /plan
request:
  question: What should we plan?
phases:
  - id: intake
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	if err := os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), []byte(projectYAML), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	store := workflowdrafts.NewMemory()
	sessionYAML := `id: plan
version: 1.0.0
name: session-plan
request:
  question: What should we plan?
phases:
  - id: intake
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(sessionYAML), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}

	resolver := Resolver{SessionStore: store, ProjectTierApplies: func(context.Context, string) bool { return true }}
	reg, scopes, err := resolver.Resolve(ctx, projectDir, "sess-1")
	testutil.FailErr(t, "resolver.Resolve failed", err)
	key := workflowdef.ManifestKey("plan", "1.0.0")
	if scopes[key] != string(api.WorkflowScopeSession) {
		t.Fatalf("scope = %q want session", scopes[key])
	}
	m, err := reg.Get("plan", "1.0.0")
	testutil.FailErr(t, "reg.Get failed", err)
	if m.Name != "session-plan" {
		t.Fatalf("name = %q want session-plan", m.Name)
	}

	regNoSession, scopesNoSession, err := resolver.Resolve(ctx, projectDir, "")
	testutil.FailErr(t, "resolver.Resolve failed", err)
	if scopesNoSession[key] != string(api.WorkflowScopeProject) {
		t.Fatalf("scope without session = %q want project", scopesNoSession[key])
	}
	mProject, err := regNoSession.Get("plan", "1.0.0")
	testutil.FailErr(t, "regNoSession.Get failed", err)
	if mProject.Name != "project-plan" {
		t.Fatalf("project name = %q", mProject.Name)
	}
}

func TestResolveSessionExtendsChain(t *testing.T) {
	store := workflowdrafts.NewMemory()
	childYAML := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: expand
    activity_label: Test phase
    next: approve
`
	ctx := context.Background()
	if err := store.Upsert(ctx, "sess-1", []byte(childYAML), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}

	resolver := Resolver{SessionStore: store, ProjectTierApplies: func(context.Context, string) bool { return true }}
	reg, scopes, err := resolver.Resolve(ctx, "", "sess-1")
	testutil.FailErr(t, "resolver.Resolve failed", err)
	key := workflowdef.ManifestKey("hotfix-session", "1.0.0")
	if scopes[key] != string(api.WorkflowScopeSession) {
		t.Fatalf("scope = %q", scopes[key])
	}
	m, err := reg.Get("hotfix-session", "1.0.0")
	testutil.FailErr(t, "reg.Get failed", err)
	if len(m.Phases) != 5 || m.Phases[0] != "research" || m.Phases[len(m.Phases)-1] != "done" {
		t.Fatalf("phases = %v want the complete inherited Plan lifecycle", m.Phases)
	}
	if m.Phases[2] != "approve" || m.Phases[len(m.Phases)-2] != "execute" {
		t.Fatalf("phases = %v want approval before execute", m.Phases)
	}
	if len(m.AllowedAgents) == 0 {
		t.Fatal("expected inherited allowed_agents from plan@1.0.0")
	}
}

func TestParseManifestYAMLFromStoredBlob(t *testing.T) {
	m, err := workflowdef.ParseManifestYAML([]byte(`id: hotfix-session
version: 1.0.0
phases:
  - id: only
    activity_label: Test phase
    complete_when: plan_stub_valid
`))
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	if m.ID != "hotfix-session" || m.Version != "1.0.0" {
		t.Fatalf("id/version = %s@%s", m.ID, m.Version)
	}
}

func TestResolveExcludesInvalidProjectManifests(t *testing.T) {
	projectDir := t.TempDir()
	workflowsDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")

	// 1. Invalid overlay with attach.policy session_create
	attachDir := filepath.Join(workflowsDir, "implement-dispatch")
	testutil.FailErr(t, "mkdir", os.MkdirAll(attachDir, 0o755))
	attachYAML := `id: implement-dispatch
version: 1.0.0
extends: implement@1.0.0
controls:
  default_execution_mode: orchestrate
attach:
  policy: session_create
`
	testutil.FailErr(t, "write attach", os.WriteFile(filepath.Join(attachDir, "workflow.yaml"), []byte(attachYAML), 0o644))

	// 2. Invalid overlay with unknown extends parent
	badExtendsDir := filepath.Join(workflowsDir, "bad-extends")
	testutil.FailErr(t, "mkdir", os.MkdirAll(badExtendsDir, 0o755))
	badExtendsYAML := `id: bad-extends
version: 1.0.0
extends: nonexistent@1.0.0
`
	testutil.FailErr(t, "write bad extends", os.WriteFile(filepath.Join(badExtendsDir, "workflow.yaml"), []byte(badExtendsYAML), 0o644))

	// 3. Unparseable YAML
	syntaxErrDir := filepath.Join(workflowsDir, "syntax-error")
	testutil.FailErr(t, "mkdir", os.MkdirAll(syntaxErrDir, 0o755))
	syntaxErrYAML := `id: syntax-error
version: 1.0.0
phases: [unclosed
`
	testutil.FailErr(t, "write syntax error", os.WriteFile(filepath.Join(syntaxErrDir, "workflow.yaml"), []byte(syntaxErrYAML), 0o644))

	// 4. Valid overlay workflow
	validDir := filepath.Join(workflowsDir, "custom-valid")
	testutil.FailErr(t, "mkdir", os.MkdirAll(validDir, 0o755))
	validYAML := `id: custom-valid
version: 1.0.0
name: Custom Valid
request:
  question: What to do?
phases:
  - id: only
    activity_label: Running
    complete_when: always
    terminal: true
`
	testutil.FailErr(t, "write valid", os.WriteFile(filepath.Join(validDir, "workflow.yaml"), []byte(validYAML), 0o644))

	resolver := Resolver{
		ProjectTierApplies: func(context.Context, string) bool { return true },
	}
	ctx := context.Background()

	// Verify Resolve() succeeds and only excludes invalid manifests
	reg, scopes, err := resolver.Resolve(ctx, projectDir, "")
	testutil.FailErr(t, "resolver.Resolve failed", err)

	// Bundled implement must be present and still bundled
	implKey := workflowdef.ManifestKey("implement", "1.0.0")
	if scopes[implKey] != string(api.WorkflowScopeBundled) {
		t.Fatalf("implement scope = %q want bundled", scopes[implKey])
	}
	if _, err := reg.Get("implement", "1.0.0"); err != nil {
		t.Fatalf("implement@1.0.0 missing: %v", err)
	}

	// Valid overlay must be present
	validKey := workflowdef.ManifestKey("custom-valid", "1.0.0")
	if scopes[validKey] != string(api.WorkflowScopeProject) {
		t.Fatalf("custom-valid scope = %q want project", scopes[validKey])
	}
	if _, err := reg.Get("custom-valid", "1.0.0"); err != nil {
		t.Fatalf("custom-valid missing: %v", err)
	}

	// Invalid ones must NOT be present
	if _, err := reg.Get("implement-dispatch", "1.0.0"); err == nil {
		t.Fatal("expected implement-dispatch to be excluded from registry")
	}
	if _, err := reg.Get("bad-extends", "1.0.0"); err == nil {
		t.Fatal("expected bad-extends to be excluded from registry")
	}
	if _, err := reg.Get("syntax-error", "1.0.0"); err == nil {
		t.Fatal("expected syntax-error to be excluded from registry")
	}

	// Verify ResolveWithExcluded returns diagnostics for each excluded manifest
	_, _, excluded, err := resolver.ResolveWithExcluded(ctx, projectDir, "")
	testutil.FailErr(t, "ResolveWithExcluded", err)

	if len(excluded) != 3 {
		t.Fatalf("excluded count = %d, want 3", len(excluded))
	}

	excludedByPath := map[string]api.ExcludedWorkflow{}
	for _, ex := range excluded {
		excludedByPath[filepath.Base(filepath.Dir(ex.Path))] = ex
	}

	// Check implement-dispatch diagnostic
	dispatchEx, ok := excludedByPath["implement-dispatch"]
	if !ok {
		t.Fatal("implement-dispatch missing from excluded")
	}
	if len(dispatchEx.Errors) == 0 || dispatchEx.Errors[0].Code != "attach_session_create_on_overlay" {
		t.Fatalf("implement-dispatch error = %+v want attach_session_create_on_overlay", dispatchEx.Errors)
	}

	// Check bad-extends diagnostic
	badExtEx, ok := excludedByPath["bad-extends"]
	if !ok {
		t.Fatal("bad-extends missing from excluded")
	}
	if len(badExtEx.Errors) == 0 || badExtEx.Errors[0].Code != "unknown_extends_parent" {
		t.Fatalf("bad-extends error = %+v want unknown_extends_parent", badExtEx.Errors)
	}

	// Check syntax-error diagnostic
	syntaxEx, ok := excludedByPath["syntax-error"]
	if !ok {
		t.Fatal("syntax-error missing from excluded")
	}
	if len(syntaxEx.Errors) == 0 || syntaxEx.Errors[0].Code != "load_error" {
		t.Fatalf("syntax-error error = %+v want load_error", syntaxEx.Errors)
	}
}

func TestExcludedManifestRetainsDiagnosticPathWithoutInvalidIdentity(t *testing.T) {
	var excluded []api.ExcludedWorkflow
	candidates := make(manifestCandidates)
	path := ".paintedwolf/workflows/invalid/workflow.yaml"
	candidates.parse([]byte("id: invalid/name\nversion: 1.0.0\n"), path, string(api.WorkflowScopeProject), &excluded)
	if len(excluded) != 1 || excluded[0].Path != path || len(excluded[0].Errors) == 0 || excluded[0].ID != "" {
		t.Fatalf("invalid manifest diagnostic = %+v", excluded)
	}
}
