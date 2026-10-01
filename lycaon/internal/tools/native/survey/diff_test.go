package survey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

func TestUnifiedDiffBasic(t *testing.T) {
	old := []string{"alpha", "beta", "gamma"}
	newLines := []string{"alpha", "BETA", "gamma"}
	out := sourceview.UnifiedDiff("a.txt", "b.txt", old, newLines, 3)
	if !strings.Contains(out, "--- a.txt") || !strings.Contains(out, "+++ b.txt") {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "- beta") || !strings.Contains(out, "+ BETA") {
		t.Fatalf("out = %q", out)
	}
}

func TestUnifiedDiffIdenticalEmpty(t *testing.T) {
	lines := []string{"same", "lines"}
	if out := sourceview.UnifiedDiff("a", "b", lines, lines, 3); out != "" {
		t.Fatalf("out = %q want empty", out)
	}
}

func TestDiffToolComparesFiles(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath := filepath.Join(tmpDir, "old.go")
	newPath := filepath.Join(tmpDir, "new.go")
	if err := os.WriteFile(oldPath, []byte("package old\n\nfunc A() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write old", err)
	}
	if err := os.WriteFile(newPath, []byte("package new\n\nfunc A() {}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write new", err)
	}
	tool := &DiffTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path_a": "old.go",
		"path_b": "new.go",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "diff files", err)
	if !strings.Contains(out, `"path_a":"old.go"`) || !strings.Contains(out, `- package old`) {
		t.Fatalf("out = %q", out)
	}
}

func TestDiffToolRejectsOversizedFile(t *testing.T) {
	tmpDir := t.TempDir()
	big := strings.Repeat("x", hostDiffMaxPathBytes+1)
	if err := os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(big), 0o644); err != nil {
		testutil.FailErr(t, "write big", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "small.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write small", err)
	}
	tool := &DiffTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path_a": "big.txt",
		"path_b": "small.txt",
	}, nativefixture.Context(tmpDir))
	if err == nil || !strings.Contains(err.Error(), "DIFF_FILE_TOO_LARGE") {
		t.Fatalf("err = %v want DIFF_FILE_TOO_LARGE", err)
	}
}

func TestDiffToolTruncatesLargeOutput(t *testing.T) {
	tmpDir := t.TempDir()
	var oldB strings.Builder
	var newB strings.Builder
	for i := range 5000 {
		fmtLine := strings.Repeat("x", 80)
		oldB.WriteString(fmtLine)
		oldB.WriteByte('\n')
		newB.WriteString(fmtLine)
		newB.WriteByte('\n')
		if i%2 == 0 {
			newB.WriteString("changed-line\n")
		}
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(oldB.String()), 0o644); err != nil {
		testutil.FailErr(t, "write a", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte(newB.String()), 0o644); err != nil {
		testutil.FailErr(t, "write b", err)
	}
	tool := &DiffTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path_a": "a.txt",
		"path_b": "b.txt",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "diff truncate", err)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("out should be truncated: len=%d", len(out))
	}
}

func TestTruncateDiffOutput(t *testing.T) {
	s := strings.Repeat("a", 300)
	out, truncated := sourceview.TruncateDiffOutput(s, 256)
	if !truncated || len(out) > 256 {
		t.Fatalf("out len=%d truncated=%v", len(out), truncated)
	}
}
