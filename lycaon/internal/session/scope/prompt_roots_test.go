package scope_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/pkg/api"
)

func promptRootsService(t *testing.T, rootCount int) (*scope.Service, *api.Session, string) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	base := t.TempDir()
	roots := make([]project.AttachRootParams, rootCount)
	for i := range roots {
		dir := filepath.Join(base, fmt.Sprintf("root-%03d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir root %d: %v", i, err)
		}
		label := fmt.Sprintf("root-%03d", i)
		if i == 0 {
			label = "app"
		}
		roots[i] = project.AttachRootParams{Path: dir, Label: label}
	}
	created, err := reg.Create(ctx, project.CreateParams{Roots: roots})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc := scope.New(nil)
	svc.SetProjects(reg)
	return svc, &api.Session{ID: "sess", ProjectID: created.ID}, base
}

func TestPromptRootRowsMarkActiveRootByIDForWorkerBranchPath(t *testing.T) {
	svc, sess, base := promptRootsService(t, 2)
	draftID := ""
	for _, ref := range mustRootRefs(t, svc, sess) {
		if ref.Label == "root-001" {
			draftID = ref.ID
		}
	}
	if draftID == "" {
		t.Fatal("fixture missing draft root")
	}
	// A worker session's WorkspacePath is a branch directory, not a root path.
	sess.WorkspaceRootID = draftID
	sess.WorkspacePath = filepath.Join(base, "worker-branches", "root-abc")

	rows, count, activePath, omitted := svc.PromptRootRows(context.Background(), sess)
	if count != 2 || omitted != 0 {
		t.Fatalf("count=%d omitted=%d", count, omitted)
	}
	if activePath == sess.WorkspacePath || activePath == "" {
		t.Fatalf("activePath=%q must be the active root path", activePath)
	}
	if rows[1]["is_active"] != true || rows[0]["is_active"] != false {
		t.Fatalf("active row mismatch: %+v", rows)
	}
	if rows[0]["is_primary"] != true || rows[1]["is_primary"] != false {
		t.Fatalf("primary flags: %+v", rows)
	}
	if _, ok := rows[0]["path"]; !ok {
		t.Fatalf("row missing path: %+v", rows[0])
	}
}

func TestPromptRootRowsEmptyWorkspaceRootIDFallsBackToPrimary(t *testing.T) {
	svc, sess, _ := promptRootsService(t, 2)
	rows, count, activePath, omitted := svc.PromptRootRows(context.Background(), sess)
	if count != 2 || omitted != 0 {
		t.Fatalf("count=%d omitted=%d", count, omitted)
	}
	if rows[0]["is_active"] != true || rows[1]["is_active"] != false {
		t.Fatalf("primary row not active: %+v", rows)
	}
	if activePath == "" {
		t.Fatal("activePath empty")
	}
}

func TestPromptRootRowsCapsAndCountsOmitted(t *testing.T) {
	svc, sess, _ := promptRootsService(t, pongoplain.MaxCollectionItems+1)
	// Keep the active root inside the capped window.
	refs := mustRootRefs(t, svc, sess)
	sess.WorkspaceRootID = refs[1].ID

	rows, count, _, omitted := svc.PromptRootRows(context.Background(), sess)
	if count != pongoplain.MaxCollectionItems+1 {
		t.Fatalf("count=%d", count)
	}
	if len(rows) != pongoplain.MaxCollectionItems {
		t.Fatalf("rows=%d want %d", len(rows), pongoplain.MaxCollectionItems)
	}
	if omitted != 1 {
		t.Fatalf("omitted=%d want 1", omitted)
	}
	if rows[1]["is_active"] != true {
		t.Fatalf("active row not marked: %+v", rows[1])
	}
}

func mustRootRefs(t *testing.T, svc *scope.Service, sess *api.Session) []projectroot.RootRef {
	t.Helper()
	refs, err := svc.Roots(context.Background(), sess)
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	return refs
}
