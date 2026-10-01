package project

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTrustEnabledDefaultsOn(t *testing.T) {
	t.Parallel()
	if !TrustEnabled(Project{}, "project_mcp") {
		t.Fatal("a project with no trust map should have every surface on")
	}
	p := Project{TrustEnabled: map[string]bool{"project_mcp": false}}
	if TrustEnabled(p, "project_mcp") {
		t.Fatal("an explicit off was not honoured")
	}
	if !TrustEnabled(p, "skills") {
		t.Fatal("switching one surface off turned another off")
	}
}

func TestMergeTrustEnabledKeepsUntouchedSurfaces(t *testing.T) {
	t.Parallel()
	merged := MergeTrustEnabled(
		map[string]bool{"skills": false, "project_mcp": true},
		map[string]bool{"project_mcp": false},
	)
	if merged["skills"] != false || merged["project_mcp"] != false {
		t.Fatalf("merge = %+v", merged)
	}
	if len(merged) != 2 {
		t.Fatalf("merge invented or dropped keys: %+v", merged)
	}
}

func TestSurfaceSeenComparesStamps(t *testing.T) {
	t.Parallel()
	p := Project{TrustSeen: map[string]SeenRecord{"project_mcp": {Stamp: "a"}}}
	if !SurfaceSeen(p, "project_mcp", "a") {
		t.Fatal("matching stamp reported unseen")
	}
	if SurfaceSeen(p, "project_mcp", "b") {
		t.Fatal("moved stamp reported seen")
	}
	if SurfaceSeen(p, "skills", "a") {
		t.Fatal("never-read surface reported seen")
	}
	if !SurfaceSeen(p, "skills", "") {
		t.Fatal("empty surface reported unseen; the dot would never clear")
	}
}

func TestTrustReadBaselineDropsEmptySurfaces(t *testing.T) {
	t.Parallel()
	in := map[string]SeenRecord{"project_mcp": {}}
	out := cloneTrustReadBaseline(in)
	if _, ok := out["project_mcp"]; ok {
		t.Fatal("a surface that lost its content kept a seen record")
	}
	if _, ok := in["project_mcp"]; !ok {
		t.Fatal("snapshot mutated its input")
	}
}

func TestMarkTrustSeenRoundTrips(t *testing.T) {
	t.Parallel()
	reg := NewMemoryRegistry()
	ctx := context.Background()
	p, err := CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)
	if len(p.TrustSeen) != 0 {
		t.Fatalf("new project already had seen records: %+v", p.TrustSeen)
	}

	marked, err := reg.MarkTrustSeen(ctx, p.ID, p, map[string]SeenRecord{
		"project_mcp": {Stamp: "stamp-1"},
	})
	testutil.FailErr(t, "MarkTrustSeen", err)
	if !SurfaceSeen(*marked, "project_mcp", "stamp-1") {
		t.Fatalf("mark did not persist: %+v", marked.TrustSeen)
	}
	if SurfaceSeen(*marked, "project_mcp", "stamp-2") {
		t.Fatal("a later stamp reported as already seen")
	}
}

func TestMarkTrustSeenRejectsStaleOpening(t *testing.T) {
	t.Parallel()
	reg := NewMemoryRegistry()
	ctx := context.Background()
	p, err := CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)

	_, err = reg.MarkTrustSeen(ctx, p.ID, p, map[string]SeenRecord{"skills": {Stamp: "one"}})
	testutil.FailErr(t, "MarkTrustSeen first", err)
	after, err := reg.MarkTrustSeen(ctx, p.ID, p, map[string]SeenRecord{"skills": {Stamp: "two"}})
	if !errors.Is(err, ErrTrustReviewChanged) || after != nil {
		t.Fatalf("stale opening = %+v, %v", after, err)
	}
}

func TestSetTrustEnabledPersists(t *testing.T) {
	t.Parallel()
	reg := NewMemoryRegistry()
	ctx := context.Background()
	p, err := CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "CreateWithRoot", err)

	off, err := reg.SetTrustEnabled(ctx, p.ID, map[string]bool{"prompt_overrides": false})
	testutil.FailErr(t, "SetTrustEnabled", err)
	if TrustEnabled(*off, "prompt_overrides") {
		t.Fatal("surface stayed on after being switched off")
	}
	if !TrustEnabled(*off, "skills") {
		t.Fatal("switching one surface off disabled another")
	}
}
