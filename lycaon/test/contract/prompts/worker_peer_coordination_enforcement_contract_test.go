package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var forbiddenLegBriefPhrases = []string{
	"Communication Strategy",
	"check peer file state",
	"read what other workers wrote",
	"coordinate on edits",
}

func TestLegBriefTemplatesForbidPeerFileStateProse(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scanDirs := []string{
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts"),
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "implement", "agents", "prompts"),
		filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials"),
	}
	var hits []string
	for _, dir := range scanDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			text := string(raw)
			for _, phrase := range forbiddenLegBriefPhrases {
				if strings.Contains(text, phrase) {
					hits = append(hits, path+" contains "+phrase)
				}
			}
			return nil
		})
	}
	if len(hits) > 0 {
		t.Fatalf("forbidden leg-brief phrases in coordinator templates:\n%s", strings.Join(hits, "\n"))
	}
}

func TestDispatchSurfaceIncludesParallelFanoutPartial(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	at := catalogfixture.FindStockAgentPrompt(t, "coordinator-mode-implement-dispatch.md")
	raw, err := at.Read()
	contractcheck.FailErr(t, "read dispatch mode", err)
	text := string(raw)
	if !strings.Contains(text, `partials/coordinator-parallel-fanout-dispatch.md`) {
		t.Fatal("implement-dispatch must include coordinator-parallel-fanout-dispatch partial")
	}
	partialPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordinator-parallel-fanout-dispatch.md")
	partial, err := os.ReadFile(partialPath)
	contractcheck.FailErr(t, "read fanout partial", err)
	for _, want := range []string{"after_workers", "preview_overlay", "promote_overlay", "shared_context"} {
		if !strings.Contains(string(partial), want) {
			t.Fatalf("fanout partial missing %q", want)
		}
	}
}

func TestRoutingSurfaceIncludesParallelReadScoutPartial(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "implement", "agents", "prompts", "coordinator-mode-implement-routing.md")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read routing mode", err)
	text := string(raw)
	if !strings.Contains(text, `partials/coordinator-parallel-read-scout-fanout.md`) {
		t.Fatal("implement-routing must include coordinator-parallel-read-scout-fanout partial")
	}
	partialPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials", "coordinator-parallel-read-scout-fanout.md")
	partial, err := os.ReadFile(partialPath)
	contractcheck.FailErr(t, "read read-scout fanout partial", err)
	for _, want := range []string{"path-explorer", "scope.mode: read", "all_workers_idle", "internal/auth/"} {
		if !strings.Contains(string(partial), want) {
			t.Fatalf("read-scout fanout partial missing %q", want)
		}
	}
}

func TestRecordFindingAppendUsesDelegationScopeNotOverlayToolDir(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "native", "reporting", "record_finding.go")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read record_finding.go", err)
	text := string(raw)
	if !strings.Contains(text, "scopeKey(ctx, tctx.SessionID)") {
		t.Fatal("record_finding must resolve delegation scope via FindingsScopeKey(sessionID)")
	}
	if strings.Contains(text, "return strings.TrimSpace(tctx.ProjectDir)") {
		t.Fatal("record_finding must not fall back to tctx.ProjectDir for findings scope")
	}
	if strings.Contains(text, "store.Append(tctx.ProjectDir") {
		t.Fatal("record_finding must not append findings keyed on tctx.ProjectDir overlay")
	}
}

func TestHandoffInitKeysOffSessionProjectDirNotOverlay(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "call", "register_reservations.go")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read register_reservations.go", err)
	text := string(raw)
	if strings.Contains(text, "return strings.TrimSpace(tctx.ProjectDir)") {
		t.Fatal("handoff_init must not fall back to tctx.ProjectDir for delegation scope")
	}
}

func TestWorkerPromptsDoNotSteerOverlayFilesystemReads(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts")
	var hits []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if strings.Contains(path, "coordinator-") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		if strings.Contains(text, "read(.paintedwolf/overlays") || strings.Contains(text, "read peer overlay") {
			hits = append(hits, path)
		}
		return nil
	})
	if len(hits) > 0 {
		t.Fatalf("worker prompts must not steer overlay FS reads:\n%s", strings.Join(hits, "\n"))
	}
}
