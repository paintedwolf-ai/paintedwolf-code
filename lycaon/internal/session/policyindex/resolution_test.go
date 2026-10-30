package policyindex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type policyRoots struct {
	roots    []projectroot.RootRef
	disabled bool
	err      error
}

func (r *policyRoots) Applies(context.Context, string, *api.Session) bool { return !r.disabled }
func (r *policyRoots) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return r.roots, r.err
}
func (r *policyRoots) ProjectRoots(context.Context, string) []projectroot.RootRef { return r.roots }
func (r *policyRoots) ActivePath(_ context.Context, sess *api.Session) (string, error) {
	root, err := projectroot.ActiveRoot(r.roots, sess.WorkspaceRootID)
	return root.Path, err
}

type policySessions struct{ session *api.Session }

func (s policySessions) Get(context.Context, string) (*api.Session, error) { return s.session, nil }

func policyFixture(t *testing.T) (*Service, *api.Session, *policyRoots) {
	t.Helper()
	roots := &policyRoots{roots: []projectroot.RootRef{
		{ID: "first", Label: "app", Path: t.TempDir(), IsPrimary: true},
		{ID: "second", Label: "library", Path: t.TempDir()},
	}}
	sess := &api.Session{ID: "chat", ProjectID: "project", WorkspaceRootID: "first"}
	m := New(policySessions{session: sess}, roots)
	m.SetRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	return m, sess, roots
}

func writePolicy(t *testing.T, root, path, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(path))
	testutil.FailErr(t, "create policy directory", os.MkdirAll(filepath.Dir(abs), 0o700))
	testutil.FailErr(t, "write policy", os.WriteFile(abs, []byte(body), 0o600))
}

func TestResolveDeduplicatesPoliciesAndPreservesRootJurisdiction(t *testing.T) {
	m, sess, roots := policyFixture(t)
	first, second := roots.roots[0], roots.roots[1]
	writePolicy(t, first.Path, "AGENTS.md", "First root policy")
	writePolicy(t, first.Path, "backend/AGENTS.md", "Backend policy")
	writePolicy(t, first.Path, "frontend/AGENTS.md", "Frontend policy")
	writePolicy(t, first.Path, ".paintedwolf/AGENTS.md", "First overlay policy")
	writePolicy(t, second.Path, "AGENTS.md", "Second root policy")
	delivery, err := m.Resolve(t.Context(), sess, []agentpolicy.Scope{
		{RootID: first.ID, RootPath: first.Path, Path: "backend/new.go"},
		{RootID: second.ID, RootPath: second.Path, Path: "."},
		{RootID: first.ID, RootPath: first.Path, Path: "frontend/new.ts"},
		{RootID: first.ID, RootPath: first.Path, Path: "backend/other.go"},
	})
	testutil.FailErr(t, "resolve policy scopes", err)
	if delivery.Status != StatusLoaded || len(delivery.Messages) != 5 || len(delivery.Manifest.Files) != 5 {
		t.Fatalf("delivery shape = %+v", delivery)
	}
	var bodies []string
	for _, message := range delivery.Messages {
		bodies = append(bodies, message.Content)
		if message.SourceContext == nil || len(message.SourceContext.Locations) != 1 {
			t.Fatal("policy source identity missing")
		}
	}
	joined := strings.Join(bodies, "\n")
	if strings.Count(joined, "First root policy") != 1 || !strings.Contains(joined, "@library/AGENTS.md") || !strings.Contains(joined, "@app/backend") {
		t.Fatalf("policy scope rendering = %s", joined)
	}
	if !strings.Contains(bodies[3], "First overlay policy") || !strings.Contains(bodies[3], "@app/.") {
		t.Fatal("overlay did not retain root jurisdiction and last precedence")
	}
	if got := m.ManifestFor(delivery.Messages); !reflect.DeepEqual(got, delivery.Manifest) {
		t.Fatalf("delivered manifest = %+v, want %+v", got, delivery.Manifest)
	}
}

func TestCurrentPolicyDetectsEditsNewAncestorsAndWorkspaceReplacement(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Root policy")
	scope := agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: "backend/file.go"}
	delivery, err := m.Resolve(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "initial policy delivery", err)
	current, _, err := m.Current(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "current policy", err)
	if !delivery.Manifest.Covers(current) {
		t.Fatal("current policy was not covered")
	}
	writePolicy(t, root.Path, "backend/AGENTS.md", "Nested policy")
	current, _, err = m.Current(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "new ancestor policy", err)
	if delivery.Manifest.Covers(current) {
		t.Fatal("new ancestor policy was treated as delivered")
	}
	delivery, err = m.Resolve(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "nested policy delivery", err)
	writePolicy(t, root.Path, "backend/AGENTS.md", "Changed nested policy")
	current, _, err = m.Current(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "changed ancestor policy", err)
	if delivery.Manifest.Covers(current) {
		t.Fatal("changed policy was treated as delivered")
	}
	replacement := t.TempDir()
	writePolicy(t, replacement, "AGENTS.md", "Root policy")
	roots.roots[0].Path = replacement
	if _, _, err := m.Current(t.Context(), sess, []agentpolicy.Scope{scope}); err == nil {
		t.Fatal("replaced workspace accepted an older physical scope")
	}
	scope.RootPath = replacement
	current, _, err = m.Current(t.Context(), sess, []agentpolicy.Scope{scope})
	testutil.FailErr(t, "replacement workspace policy", err)
	if delivery.Manifest.Covers(current) {
		t.Fatal("identical bytes in another workspace reused the policy delivery")
	}
}
