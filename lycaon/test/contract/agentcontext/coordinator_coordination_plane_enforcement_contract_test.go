package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Half-A correctness-plane enforcement scans. The flow-plane literal
// containment scan lives in coordinator_flow_enforcement_contract_test.go.

var forbiddenShadowBoardPaths = []string{
	settingsoverlay.Rel("plans/board.md"),
	settingsoverlay.Rel("plans/board.plan.md"),
	settingsoverlay.Rel("scratchpad/board.md"),
	settingsoverlay.Rel("workbook/workbook.md"),
}

func TestCoordinationPlaneEnforcement_oneBoardAuthoringPath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	if progress.AuthoringToolID != "update_progress" {
		t.Fatalf("AuthoringToolID = %q want update_progress", progress.AuthoringToolID)
	}

	reg, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "LoadPathScopes", err)
	productWrite, ok := reg["coordinator_product_write"]
	if !ok {
		t.Fatal("missing coordinator_product_write scope")
	}
	// The overlay is writable under approval, so the one-board rule is the
	// shadow-board rejection rather than a path-scope deny.
	if err := sandbox.CheckWriteInScope(productWrite, "coordinator_product_write", settingsoverlay.DirName()+"/plans/board.md"); err != nil {
		t.Fatalf("coordinator_product_write must reach the overlay for review: %v", err)
	}
	for _, shadow := range progressShadowBoardPaths() {
		if !progress.IsShadowBoardPath(shadow) {
			t.Fatalf("%q must be rejected as a shadow progress board", shadow)
		}
	}

	writeScopePath := filepath.Join(root, "lycaon", "internal", "tools", "native", "coordinator_write_scope.go")
	raw, err := os.ReadFile(writeScopePath)
	contractcheck.FailErr(t, "read coordinator_write_scope.go", err)
	text := string(raw)
	if !strings.Contains(text, "progress.IsShadowBoardPath(path)") {
		t.Fatal("investigate write scope must reject shadow board paths via progress.IsShadowBoardPath")
	}

	scanDirs := []string{
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts"),
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials"),
	}
	var hits []string
	for _, dir := range scanDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			if !strings.Contains(path, "coordinator") {
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			content := string(body)
			for _, shadow := range forbiddenShadowBoardPaths {
				if strings.Contains(content, shadow) {
					hits = append(hits, path+" references shadow board "+shadow)
				}
			}
			return nil
		})
	}
	if len(hits) > 0 {
		t.Fatalf("coordinator templates must not name shadow board paths:\n%s", strings.Join(hits, "\n"))
	}
}

func progressShadowBoardPaths() []string {
	return []string{
		settingsoverlay.Rel("plans/board.md"), settingsoverlay.Rel("plans/board.plan.md"),
		settingsoverlay.Rel("scratchpad/board.md"), settingsoverlay.Rel("workbook/workbook.md"),
	}
}
