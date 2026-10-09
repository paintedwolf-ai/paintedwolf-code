package survey

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestLayoutSnapshotScalesByFoldingCompleteRollups(t *testing.T) {
	const (
		groupCount    = 200
		filesPerGroup = 500
		groupCap      = 32
	)
	entries := make([]sourcecatalog.Entry, 0, groupCount*(filesPerGroup+1))
	for group := range groupCount {
		dir := fmt.Sprintf("component-%03d", group)
		entries = append(entries, sourcecatalog.Entry{RootID: "r1", Path: dir, Name: dir, IsDir: true})
		for file := range filesPerGroup {
			name := fmt.Sprintf("file-%03d.go", file)
			entries = append(entries, sourcecatalog.Entry{
				RootID: "r1", Path: dir + "/" + name, Parent: dir, Name: name, Size: 100,
			})
		}
	}
	snapshot := sourcecatalog.NewSnapshot([]sourcecatalog.Root{{ID: "r1", Path: "/repo"}}, entries)
	records, examined, coverage, err := layoutRecordsFromSnapshot(t.Context(), "layout", snapshot, "r1", ".", ".", nil, groupCap)
	testutil.FailErr(t, "layout snapshot", err)
	if examined != len(entries) || coverage.EntriesExamined != len(entries) {
		t.Fatalf("examined = %d coverage=%d want %d", examined, coverage.EntriesExamined, len(entries))
	}
	if coverage.Groups != groupCount || coverage.GroupsFolded != groupCount-(groupCap-1) {
		t.Fatalf("coverage = %+v", coverage)
	}
	if len(records) != groupCap {
		t.Fatalf("records = %d want %d", len(records), groupCap)
	}
	if coverage.Resolution != "subtree_rollups" || coverage.InventoryEntries != len(entries) {
		t.Fatalf("coverage = %+v", coverage)
	}
	if got := records[0]; got.Kind != "rollup" || !strings.Contains(got.Body[0], "overview groups=200 folded_groups=") {
		t.Fatalf("first record = %+v, want complete overview before drill records", got)
	}
}

func TestShallowLayoutIsCompleteAtImmediateChildResolution(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"browser", "dom", "js"} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Join(root, dir, "deep"), 0o755))
	}
	testutil.FailErr(t, "write readme", os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o644))
	records, total, coverage, err := shallowLayoutRecords("layout", root, ".", ".", sourcecatalog.StateWarming, nil, 96)
	testutil.FailErr(t, "shallow layout", err)
	if total != 4 || len(records) != 4 {
		t.Fatalf("total=%d records=%d, want four immediate children", total, len(records))
	}
	if coverage.Resolution != "immediate_children" || coverage.InventoryState != "warming" || coverage.GroupsFolded != 0 {
		t.Fatalf("coverage = %+v", coverage)
	}
}

func TestLayoutProbeFileAltitudeDoesNotBuildInventory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	testutil.FailErr(t, "write file", os.WriteFile(file, []byte("package main\n"), 0o644))
	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true, RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}}})
	scope := Scope{Boundary: boundary, ToolCtx: tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}}
	records, total, coverage, err := runLayoutProbe(context.Background(), Probe{Label: "layout"}, "main.go", scope, 96)
	testutil.FailErr(t, "file layout", err)
	if total != 1 || len(records) != 1 || records[0].Path != "main.go" {
		t.Fatalf("records=%+v total=%d", records, total)
	}
	if coverage.Altitude != "file" || coverage.Resolution != "file" || coverage.InventoryState != "not_needed" {
		t.Fatalf("coverage = %+v", coverage)
	}
}
