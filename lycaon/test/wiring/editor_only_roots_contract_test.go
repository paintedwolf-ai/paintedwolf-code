package wiring

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// agentView is everything an agent consumer derives from a chat's project roots.
type agentView struct {
	ToolRoots          []projectroot.RootRef
	ActiveRootID       string
	ConfinementRoots   []string
	ScopePaths         []string
	ActivePath         string
	PromptRows         []map[string]any
	PromptRootCount    int
	PromptActivePath   string
	Scope              project.ProjectScope
	OverlayPaths       []string
	SkillNames         []string
	MCPScope           mcp.CallScope
	TrustScanRoots     []string
	RootHistoryVersion int
}

// INV-3: everything an agent consumer receives for a project with editor-only
// roots equals what it receives for the same project without them.
func TestEditorOnlyRootsDoNotExistForAgents(t *testing.T) {
	h := BuildForTest(t)
	facts := rootfacts.Install(t)
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, facts.Work)
	testutil.FailErr(t, "create session", err)
	for _, root := range facts.EditorOnly() {
		testutil.FailErr(t, "write project policy", os.WriteFile(filepath.Join(root.Path, "AGENTS.md"), []byte("policy\n"), 0o600))
	}

	before := captureAgentView(t, h, sess)
	for _, root := range facts.EditorOnly() {
		attached := attachRootOverHTTP(t, h, sess.ProjectID, root.Path)
		if attached.Admitted || attached.AdmissionCode == nil || *attached.AdmissionCode != root.Code {
			t.Fatalf("%s: attached root admitted=%v code=%v, want editor-only with %q", root.Name, attached.Admitted, attached.AdmissionCode, root.Code)
		}
	}
	after := captureAgentView(t, h, sess)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("agent view changed after attaching editor-only roots\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func captureAgentView(t *testing.T, h *Harness, sess *wire.Session) agentView {
	t.Helper()
	ctx := t.Context()
	manager := h.Sessions.Manager
	current, err := h.Store.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload session", err)
	tctx, err := manager.ToolContext.Build(ctx, current, "implement", inject.Machine{})
	testutil.FailErr(t, "build tool context", err)
	p, err := h.Server.Sources.Workspace.ProjectRegistry.Get(ctx, current.ProjectID)
	testutil.FailErr(t, "load project", err)

	view := agentView{
		ToolRoots:          tctx.Source.Roots,
		ActiveRootID:       tctx.Source.ActiveRootID,
		ConfinementRoots:   tools.ConfineRootsForAction(tctx),
		TrustScanRoots:     project.RootPaths(p),
		RootHistoryVersion: tctx.Source.RootHistory.Generation,
		MCPScope:           mcp.ProjectScope(p.ID, project.PrimaryRootPath(p), project.RootPaths(p)),
	}
	view.ScopePaths, err = manager.Workspace.Paths(ctx, current)
	testutil.FailErr(t, "scope paths", err)
	view.ActivePath, err = manager.Workspace.ActivePath(ctx, current)
	testutil.FailErr(t, "active path", err)
	view.PromptRows, view.PromptRootCount, view.PromptActivePath = manager.Workspace.PromptRootRows(ctx, current)
	view.Scope, err = project.ScopeForSession(ctx, current, h.Server.Sources.Workspace.ProjectRegistry)
	testutil.FailErr(t, "session scope", err)
	overlay, err := project.ResolveProjectOverlay(p, current.WorkspaceRootID)
	testutil.FailErr(t, "resolve overlay", err)
	view.OverlayPaths = overlay.Paths
	loaded, _ := manager.Profiles.EffectiveSkills(ctx, p.ID, project.RootPaths(p))
	for _, skill := range loaded {
		view.SkillNames = append(view.SkillNames, skill.Name)
	}
	return view
}

func attachRootOverHTTP(t *testing.T, h *Harness, projectID, path string) wire.ProjectRoot {
	t.Helper()
	body, err := json.Marshal(wire.AttachProjectRootRequest{Path: path})
	testutil.FailErr(t, "encode attach", err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects/"+projectID+"/roots", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("attach %s: status %d body %s", path, w.Code, w.Body.String())
	}
	var p wire.Project
	testutil.FailErr(t, "decode project", json.Unmarshal(w.Body.Bytes(), &p))
	for _, root := range p.Roots {
		if root.Path == path {
			return root
		}
	}
	t.Fatalf("attached root %s missing from response", path)
	return wire.ProjectRoot{}
}
