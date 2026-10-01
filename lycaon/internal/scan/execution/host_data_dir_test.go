package execution

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHostDataDirUsesProjectIDWhenKnown(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	rootPath := project.PrimaryRootPath(p)

	dataDir := t.TempDir()
	r := &Runner{
		DataDir: dataDir,
		Events:  &events.Publisher{Lookup: project.ScopeLookup{Registry: reg}},
	}

	got := r.hostDataDir(ctx, &api.CodeScan{ID: "scan-1", CanonicalPath: rootPath})
	want := project.HostDataDir(dataDir, p.ID)
	if got != want {
		t.Fatalf("host data dir = %q, want the project-keyed %q", got, want)
	}
	if strings.Contains(got, "path-") {
		t.Fatalf("scan evidence still keyed by path hash: %q", got)
	}
}

func TestHostDataDirFallsBackToPathKey(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()

	dataDir := t.TempDir()
	unowned := t.TempDir()
	r := &Runner{
		DataDir: dataDir,
		Events:  &events.Publisher{Lookup: project.ScopeLookup{Registry: reg}},
	}

	got := r.hostDataDir(ctx, &api.CodeScan{ID: "scan-1", CanonicalPath: unowned})
	want := project.PathKeyedHostDataDir(dataDir, unowned)
	if got != want {
		t.Fatalf("host data dir = %q, want the path-keyed fallback %q", got, want)
	}
	if filepath.Base(got) == "" || !strings.HasPrefix(filepath.Base(got), "path-") {
		t.Fatalf("expected a path- keyed directory, got %q", got)
	}
}

func TestHostDataDirWithoutPublisherStillResolves(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	r := &Runner{DataDir: dataDir}

	got := r.hostDataDir(context.Background(), &api.CodeScan{ID: "scan-1", CanonicalPath: workspace})
	if got != project.PathKeyedHostDataDir(dataDir, workspace) {
		t.Fatalf("host data dir = %q, want the path-keyed fallback", got)
	}
}
