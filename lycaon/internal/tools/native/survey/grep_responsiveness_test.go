package survey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGrepArtifactTreeSelectsBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	for _, folder := range []string{"target/debug", "node_modules/package", ".cache/build"} {
		testutil.FailErr(t, "create artifact tree", os.MkdirAll(filepath.Join(dir, folder), 0o755))
		for i := range 80 {
			file, err := os.Create(filepath.Join(dir, folder, fmt.Sprintf("artifact-%03d.o", i)))
			testutil.FailErr(t, "create artifact", err)
			testutil.FailErr(t, "size sparse artifact", file.Truncate(32<<20))
			testutil.FailErr(t, "close artifact", file.Close())
		}
		testutil.FailErr(t, "write generated text", os.WriteFile(filepath.Join(dir, folder, "generated.ts"), []byte("RELEASE_BUILD\n"), 0o644))
	}
	testutil.FailErr(t, "write gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("target/\nnode_modules/\n.cache/\n"), 0o644))
	filter, err := sandbox.CompileEntryGlob("**/*.{yml,yaml,sh,json,toml,go,rs,ts}")
	testutil.FailErr(t, "compile selection", err)
	re, err := compileGrepRegex("RELEASE_BUILD", false)
	testutil.FailErr(t, "compile pattern", err)
	search := &grepSearch{ctx: t.Context(), reads: projectpaths.NewReadSession(nativefixture.Boundary(t), nativefixture.Context(dir)), re: re, pathFilter: filter, maxMatches: 2000, resp: &grepResponse{}, stats: &grepEngineStats{}}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, targets, err := tool.resolveGrepTargets(t.Context(), nativefixture.Context(dir), map[string]any{"path": "."})
	testutil.FailErr(t, "resolve targets", err)
	for _, target := range targets {
		testutil.FailErr(t, "search artifacts", tool.grepWalkTree(t.Context(), nativefixture.Context(dir), target, true, search, sandbox.SurveyOptions{}))
	}
	if len(search.resp.Matches) != 3 || search.stats.FilesOpened.Load() != 3 {
		t.Fatalf("matches=%+v opened=%d; all generated text and no excluded artifact should be read", search.resp.Matches, search.stats.FilesOpened.Load())
	}
	search.pathFilter = sandbox.EntryGlob{}
	search.resp = &grepResponse{}
	for _, target := range targets {
		testutil.FailErr(t, "search all artifacts", tool.grepWalkTree(t.Context(), nativefixture.Context(dir), target, true, search, sandbox.SurveyOptions{}))
	}
	if search.binarySkipped != 240 || len(search.resp.Matches) != 3 {
		t.Fatalf("unfiltered search: skipped=%d matches=%d", search.binarySkipped, len(search.resp.Matches))
	}
}

func TestGrepCancellationJoinsReaders(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle"), 0o644))
	for range cap(grepReadSlots) {
		grepReadSlots <- struct{}{}
	}
	defer func() {
		for range cap(grepReadSlots) {
			<-grepReadSlots
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	tctx := nativefixture.Context(dir)
	tctx.ReportProgress = func(progress api.ToolProgress) {
		if progress.Phase == "searching" {
			cancel()
		}
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(ctx, map[string]any{"pattern": "needle", "path": "."}, tctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation returned %v", err)
	}
	if len(grepReadSlots) != cap(grepReadSlots) {
		t.Fatal("canceled search retained or consumed another reader's slot")
	}
}

func TestDiscoveryRejectsInvalidGlob(t *testing.T) {
	dir := t.TempDir()
	_, err := (&GrepTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"pattern": "x", "path_glob": "*.{go,ts"}, nativefixture.Context(dir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURVEY_GLOB_INVALID" {
		t.Fatalf("grep invalid glob: %v", err)
	}
	_, err = (&FindTool{Boundary: nativefixture.Boundary(t)}).Run(t.Context(), map[string]any{"name_glob": "["}, nativefixture.Context(dir))
	if !errors.As(err, &reject) || reject.Code != "SURVEY_GLOB_INVALID" {
		t.Fatalf("find invalid glob: %v", err)
	}
}

func TestGrepReadFailureDoesNotClaimAbsence(t *testing.T) {
	search := &grepSearch{ctx: t.Context(), unreadable: 1, resp: &grepResponse{}}
	opts, err := parseGrepArgs(map[string]any{"pattern": "needle"})
	testutil.FailErr(t, "parse arguments", err)
	out, err := (&GrepTool{}).finalizeGrepResponse(t.Context(), ".", search, opts, nil)
	testutil.FailErr(t, "encode incomplete search", err)
	var response grepResponse
	testutil.FailErr(t, "decode response", json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &response))
	if response.Note != "" || response.FilesUnreadable != 1 || !strings.Contains(response.TruncationBanner, "incomplete") {
		t.Fatalf("incomplete search = %+v", response)
	}
}

func TestGrepDeadlineIsNotAnEmptySearch(t *testing.T) {
	var reject *tools.ToolReject
	if err := grepExecutionError(context.DeadlineExceeded); !errors.As(err, &reject) || reject.Code != "GREP_DEADLINE_EXCEEDED" {
		t.Fatalf("deadline error = %v", err)
	}
	if err := grepExecutionError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestGrepReadSlotsAreCancelable(t *testing.T) {
	for range cap(grepReadSlots) {
		grepReadSlots <- struct{}{}
	}
	defer func() {
		for range cap(grepReadSlots) {
			<-grepReadSlots
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := readGrepFile(ctx, nil, "", nil)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued read error = %v", err)
	}
}

func TestGrepPerFileResultsAreBounded(t *testing.T) {
	re, err := compileGrepRegex("hit", false)
	testutil.FailErr(t, "compile pattern", err)
	for _, contextLines := range []int{0, 2} {
		search := &grepSearch{ctx: t.Context(), re: re, offset: 4, maxMatches: 3, contextLines: contextLines}
		matches, err := search.collectMatches("generated.txt", []byte(strings.Repeat("hit\n", 100000)))
		testutil.FailErr(t, "match dense content", err)
		if len(matches) != 7 {
			t.Fatalf("retained %d results for a seven-match window", len(matches))
		}
	}
}
