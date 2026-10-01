package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestEditToolStringReplaceEchoesSeam(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte(seed), 0o644))
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "gamma", "new_string": "GAMMA",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "edit string replace", err)
	if !strings.Contains(out, "Seam context (after edit):") {
		t.Fatalf("edit receipt missing seam context: %q", out)
	}
	if !strings.Contains(out, "3\tGAMMA") {
		t.Fatalf("seam should show the numbered edited line: %q", out)
	}
}

func TestEditToolReplaceAllSkipsSeam(t *testing.T) {
	tmpDir := t.TempDir()
	seed := "x\nx\nx\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte(seed), 0o644))
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "edit replace_all", err)
	if strings.Contains(out, "Seam context") {
		t.Fatalf("multi-occurrence replace_all has scattered seams; should omit seam: %q", out)
	}
}

func TestEditToolRejectsIdenticalStrings(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("same\n"), 0o644))
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "same", "new_string": "same",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "EDIT_ARGS_CONFLICT" {
		t.Fatalf("err = %v want EDIT_ARGS_CONFLICT", err)
	}
}

func TestEditToolMissingNewStringIsArgsInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("same\n"), 0o644))
	tool := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "same",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %v want TOOL_ARGS_INVALID", err)
	}
}
