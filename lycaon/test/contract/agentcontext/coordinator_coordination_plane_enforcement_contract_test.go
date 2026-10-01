package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Half-A correctness-plane enforcement scans. The flow-plane literal
// containment scan lives in coordinator_flow_enforcement_contract_test.go.

var forbiddenFindingsFilePhrases = []string{
	"notes/*.md",
	settingsoverlay.Rel("scratchpad/notes"),
	settingsoverlay.Rel("workbook/notes"),
	"read the worker's findings",
	"read the worker findings",
	"findings file",
	"worker's findings file",
}

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

func TestCoordinationPlaneEnforcement_noFindingsFileCopy(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
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
			text := string(body)
			for _, phrase := range forbiddenFindingsFilePhrases {
				if strings.Contains(text, phrase) {
					hits = append(hits, path+" contains "+phrase)
				}
			}
			return nil
		})
	}
	if len(hits) > 0 {
		t.Fatalf("coordinator prompts must not imply findings-as-files:\n%s", strings.Join(hits, "\n"))
	}
}

func TestCoordinationPlaneEnforcement_readOnlyWorkerDiet(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	loopPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordination-loop.md")
	raw, err := os.ReadFile(loopPath)
	contractcheck.FailErr(t, "read coordination-loop.md", err)
	loop := string(raw)
	if strings.Contains(loop, "maintain the session plan with `update_progress`") {
		t.Fatal("coordination-loop.md must not carry coordinator plan copy — investigate core defines plan guidance")
	}

	agentDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts")
	var leaks []string
	_ = filepath.Walk(agentDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if strings.Contains(path, "coordinator") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "include_coordinator_plan_copy") {
			leaks = append(leaks, path)
		}
		return nil
	})
	if len(leaks) > 0 {
		t.Fatalf("worker personas must not set include_coordinator_plan_copy:\n%s", strings.Join(leaks, "\n"))
	}

	engine := contractPersonaEngine(t)
	for _, id := range []string{"web-researcher", "repo-researcher", "path-explorer"} {
		got, renderErr := prompts.RenderPersona(context.Background(), engine, id, nil)
		contractcheck.FailErr(t, "RenderPersona "+id, renderErr)
		if strings.Contains(got, "**Coordinator:**") || strings.Contains(got, "maintain the session plan with `update_progress`") {
			t.Fatalf("%s persona must not include coordinator plan copy", id)
		}
	}
}

func TestCoordinationPlaneEnforcement_rejectCopyCapabilityGated(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	nativeDir := filepath.Join(root, "lycaon", "internal", "tools", "native")
	var violations []string
	_ = filepath.Walk(nativeDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(body)
		if strings.Contains(content, "decisions/") && !strings.Contains(content, "writeAllowed") {
			violations = append(violations, filepath.Base(path)+": mentions decisions/ without writeAllowed gate")
		}
		if strings.Contains(content, "belongs in a page") {
			violations = append(violations, filepath.Base(path)+": stale page reject copy")
		}
		return nil
	})
	if len(violations) > 0 {
		t.Fatalf("native tool reject copy must be capability-gated:\n%s", strings.Join(violations, "\n"))
	}
}

func progressShadowBoardPaths() []string {
	return []string{
		settingsoverlay.Rel("plans/board.md"), settingsoverlay.Rel("plans/board.plan.md"),
		settingsoverlay.Rel("scratchpad/board.md"), settingsoverlay.Rel("workbook/workbook.md"),
	}
}
