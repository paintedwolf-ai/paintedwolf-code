package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProjectOpenAPIRoutesAndDTOFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, route := range []string{
		"POST /v1/projects",
		"GET /v1/projects/{id}",
		"PATCH /v1/projects/{id}",
		"POST /v1/projects/{id}/removals",
		"POST /v1/projects/{id}/roots",
		"DELETE /v1/projects/{id}/roots/{root_id}",
		"PATCH /v1/projects/{id}/roots/{root_id}",
	} {
		if !openAPIHasRoute(root, route) {
			t.Fatalf("openapi missing route %q", route)
		}
	}
	for _, field := range []string{
		"session_count",
		"last_activity_at",
		"last_opened_at",
		"created_at",
		"roots_generation",
		"ProjectRoot",
		"AttachProjectRootRequest",
		"UpdateProjectRootRequest",
		"ProjectEvent",
	} {
		if !openAPISchemaContains(root, field) {
			t.Fatalf("openapi missing project field/schema %q", field)
		}
	}
}

func TestProjectListRecentFirstAndReadModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "projects-list.db")
	reg := project.NewSQLRegistry(sqlDB)

	older, err := reg.Create(ctx, project.CreateParams{})
	contractcheck.FailErr(t, "create older", err)
	time.Sleep(2 * time.Millisecond)
	newer, err := reg.Create(ctx, project.CreateParams{})
	contractcheck.FailErr(t, "create newer", err)
	name := "named"
	_, err = reg.Patch(ctx, newer.ID, project.PatchParams{Name: &name})
	contractcheck.FailErr(t, "rename newer", err)

	list, err := reg.List(ctx)
	contractcheck.FailErr(t, "list projects", err)
	if len(list) < 2 {
		t.Fatalf("list len = %d", len(list))
	}
	if list[0].ID != newer.ID {
		t.Fatalf("first project = %q want %q (recent-first)", list[0].ID, newer.ID)
	}
	foundOlder := false
	for _, p := range list {
		if p.ID == older.ID {
			foundOlder = true
		}
		if p.SessionCount != 0 {
			t.Fatalf("session_count = %d want 0 for project %q", p.SessionCount, p.ID)
		}
	}
	if !foundOlder {
		t.Fatal("older project missing from list")
	}
}

func openAPIHasRoute(root, want string) bool {
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	if err != nil {
		return false
	}
	parts := strings.SplitN(want, " ", 2)
	if len(parts) != 2 {
		return false
	}
	method, path := strings.ToUpper(parts[0]), parts[1]
	text := strings.ToLower(string(data))
	return strings.Contains(text, strings.ToLower(path)+":") && strings.Contains(text, strings.ToLower(method)+":")
}

func openAPISchemaContains(root, needle string) bool {
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), needle)
}
