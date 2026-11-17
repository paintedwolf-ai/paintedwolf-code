package contract

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const escapeFixtureContent = "outside\n"

// newEscapeFixture nests the project in base beside a real outside.txt, so the
// operand ../outside.txt names an existing file and a refusal is never a not-found.
func newEscapeFixture(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.Mkdir(root, 0o755))
	outside = filepath.Join(base, "outside.txt")
	testutil.FailErr(t, "write outside file", os.WriteFile(outside, []byte(escapeFixtureContent), 0o644))
	return root, outside
}

// assertEscapeRefused requires the tool's own path refusal and an outside file
// with its original content and mode.
func assertEscapeRefused(t *testing.T, err error, code, outside string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	info, statErr := os.Stat(outside)
	testutil.FailErr(t, "stat outside file", statErr)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("outside file mode = %v, want 0644", info.Mode().Perm())
	}
	raw, readErr := os.ReadFile(outside)
	testutil.FailErr(t, "read outside file", readErr)
	if string(raw) != escapeFixtureContent {
		t.Fatalf("outside file content = %q", raw)
	}
}

func TestChmodPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root, outside := newEscapeFixture(t)
	chmod := &native.ChmodTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := chmod.Run(t.Context(), map[string]any{
		"paths": []any{"../outside.txt"},
		"mode":  "+x",
	}, implementToolContext(root))
	assertEscapeRefused(t, err, "CHMOD_PATH_DENIED", outside)
}

func TestDeletePathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root, outside := newEscapeFixture(t)
	del := &native.DeleteTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := del.Run(t.Context(), map[string]any{
		"paths": []any{"../outside.txt"},
	}, implementToolContext(root))
	assertEscapeRefused(t, err, "DELETE_PATH_DENIED", outside)
}

func TestCopyPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root, outside := newEscapeFixture(t)
	copyTool := &native.CopyTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := copyTool.Run(t.Context(), map[string]any{
		"copies": []any{
			map[string]any{"from": "../outside.txt", "to": "copy.txt"},
		},
	}, implementToolContext(root))
	assertEscapeRefused(t, err, "COPY_PATH_DENIED", outside)
	assertAbsent(t, filepath.Join(root, "copy.txt"))
}

func TestMovePathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root, outside := newEscapeFixture(t)
	moveTool := &native.MoveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := moveTool.Run(t.Context(), map[string]any{
		"moves": []any{
			map[string]any{"from": "../outside.txt", "to": "moved.txt"},
		},
	}, implementToolContext(root))
	assertEscapeRefused(t, err, "MOVE_PATH_DENIED", outside)
	assertAbsent(t, filepath.Join(root, "moved.txt"))
}

func TestChownPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root, outside := newEscapeFixture(t)
	chownTool := &native.ChownTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := chownTool.Run(t.Context(), map[string]any{
		"paths": []any{"../outside.txt"},
		"owner": "current",
	}, implementToolContext(root))
	assertEscapeRefused(t, err, "CHOWN_PATH_DENIED", outside)
}
