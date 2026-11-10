package tools

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceCaptureResolvesProjectAndWorkerCoordinates(t *testing.T) {
	primary, secondary, branch := t.TempDir(), t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{
		{ID: "main", Path: primary, Label: "main", IsPrimary: true},
		{ID: "other", Path: secondary, Label: "other"},
	}
	dir, err := projectroot.BranchDirForID("other")
	testutil.FailErr(t, "branch directory", err)
	for _, tt := range []struct {
		name, pin, branch, root, path, display string
		roots                                  []projectroot.RootRef
	}{
		{"project", "disk:main/file.go", "", primary, "file.go", "file.go", roots},
		{"attached", "disk:other/file.go", "", secondary, "file.go", "@other/file.go", roots},
		{"worker", "disk:worker-branch/file.go", branch, branch, "file.go", "file.go", roots[:1]},
		{"worker attached", "disk:worker-branch/" + dir + "/file.go", branch, branch, dir + "/file.go", "@other/file.go", roots},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tc := ToolContext{Source: InvocationSource{Roots: tt.roots, WorkerBranchRoot: tt.branch}}
			got, ok := tc.ResolveSourceCapture(tt.pin)
			if !ok || got.Disk.Root != tt.root || got.Disk.Rel != filepath.FromSlash(tt.path) || got.Path != "file.go" || got.DisplayPath != tt.display {
				t.Fatalf("capture location = %+v, %v", got, ok)
			}
			wantRoot := "main"
			if tt.display == "@other/file.go" {
				wantRoot = "other"
			}
			if got.RootID != wantRoot {
				t.Fatalf("project root = %q, want %q", got.RootID, wantRoot)
			}
		})
	}
}

func TestSourceCaptureRejectsUnboundAndEscapingPins(t *testing.T) {
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{{ID: "main", Path: t.TempDir(), IsPrimary: true}}}}
	for _, pin := range []string{"window:file", "disk:main", "disk:missing/file", "disk:worker-branch/file", "disk:main/../outside", "disk:main/.", "disk:main//absolute"} {
		if got, ok := tc.ResolveSourceCapture(pin); ok {
			t.Fatalf("accepted %q: %+v", pin, got)
		}
	}
	tc.Source.WorkerBranchRoot = t.TempDir()
	if _, ok := tc.ResolveSourceCapture("disk:main/file"); ok {
		t.Fatal("worker capture resolved a parent workspace file")
	}
}
