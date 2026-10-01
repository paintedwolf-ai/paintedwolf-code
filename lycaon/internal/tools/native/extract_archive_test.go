package native

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func extractTestBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID: tools.DefaultToolProfileID,
		Tools: map[string]bool{
			"read": true, "write": true, "extract_archive": true,
		},
	}})
}

func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		testutil.FailErr(t, "zip create "+name, err)
		_, err = w.Write([]byte(content))
		testutil.FailErr(t, "zip write "+name, err)
	}
	testutil.FailErr(t, "zip close", zw.Close())
	testutil.FailErr(t, "write zip file", os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestExtractArchiveToolZip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "sdk.zip")
	writeTestZip(t, zipPath, map[string]string{
		"lib/foo.txt": "hello",
	})
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "sdk.zip",
		"dest": "out",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "extract", err)
	var result extractArchiveResponse
	testutil.FailErr(t, "decode extraction result", json.Unmarshal([]byte(out), &result))
	if result.Path != "sdk.zip" || result.Dest != "out" || result.Entries != 1 || result.Bytes != 5 ||
		!slices.Equal(result.Extracted, []extractEntryResult{{Path: "lib/foo.txt", Bytes: 5}}) {
		t.Fatalf("extraction result = %+v, want one five-byte file", result)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "out", "lib", "foo.txt"))
	testutil.FailErr(t, "read extracted", err)
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
}

func TestExtractArchiveToolRejectsZipSlip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "evil.zip")
	writeTestZip(t, zipPath, map[string]string{
		"../escape.txt": "bad",
	})
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "evil.zip",
		"dest": "out",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EXTRACT_ZIP_SLIP" {
		t.Fatalf("err = %v want EXTRACT_ZIP_SLIP", err)
	}
}

// Archive entries meet the same boundary as direct writes: agent-policy files
// reach review, and repository metadata is refused with its Git route.
func TestExtractArchiveToolRoutesGovernedEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		code  string
	}{
		{"approval rules", settingsoverlay.Rel("approvals.yaml"), ""},
		{"mcp catalog", settingsoverlay.Rel("mcp.yaml"), ""},
		{"git hooks entry", ".git/hooks/pre-commit", "GIT_INTERNALS_WRITE_DENIED"},
		{"git config entry", ".git/config", "GIT_INTERNALS_WRITE_DENIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			zipPath := filepath.Join(tmpDir, "payload.zip")
			writeTestZip(t, zipPath, map[string]string{tc.entry: "flag: true\n"})

			tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
			tctx := nativefixture.Context(tmpDir)
			if tc.code == "" {
				testutil.FailErr(t, "seed policy parent", os.MkdirAll(filepath.Dir(filepath.Join(tmpDir, tc.entry)), 0o755))
				tctx = decliningReview(t, tmpDir, tc.entry)
			}
			_, err := tool.Run(context.Background(), map[string]any{
				"path": "payload.zip",
				"dest": ".",
			}, tctx)

			if tc.code == "" {
				assertReviewDeclined(t, err, tc.entry)
			} else {
				var reject *tools.ToolReject
				if !errors.As(err, &reject) || reject.Code != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
			}
			if _, statErr := os.Stat(filepath.Join(tmpDir, filepath.FromSlash(tc.entry))); statErr == nil {
				t.Fatalf("%s was written without its approval", tc.entry)
			}
		})
	}
}

// Overlay files no loader reads, such as blueprints, extract as ordinary files.
func TestExtractArchiveToolAllowsNonSinkOverlayFiles(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "ok.zip")
	writeTestZip(t, zipPath, map[string]string{
		settingsoverlay.Rel("blueprints/plan.md"): "# plan\n",
		"src/main.go": "package main\n",
	})
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": "ok.zip",
		"dest": ".",
	}, nativefixture.Context(tmpDir)); err != nil {
		testutil.FailErr(t, "extract ordinary archive", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, settingsoverlay.DirName(), "blueprints", "plan.md")); err != nil {
		t.Fatalf("non-sink overlay file should extract: %v", err)
	}
}

func TestExtractArchiveToolRejectsUnsupportedFormat(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "data.tar"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "data.tar",
		"dest": "out",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EXTRACT_FORMAT_UNSUPPORTED" {
		t.Fatalf("err = %v", err)
	}
}

func writeTestTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		testutil.FailErr(t, "tar header", tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		testutil.FailErr(t, "tar content", err)
	}
	testutil.FailErr(t, "close tar", tw.Close())
	testutil.FailErr(t, "close gzip", gz.Close())
	testutil.FailErr(t, "write tar.gz", os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestSafeArchiveEntryName(t *testing.T) {
	if _, err := safeArchiveEntryName("../etc/passwd"); err == nil {
		t.Fatal("expected zip slip reject")
	}
	if got, err := safeArchiveEntryName("lib/foo.txt"); err != nil || got != "lib/foo.txt" {
		t.Fatalf("got = %q err = %v", got, err)
	}
}

func TestExtractArchiveToolRejectsAliasedSinkEntries(t *testing.T) {
	dir := settingsoverlay.DirName()
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"upper case overlay dir", strings.ToUpper(dir) + "/approvals.yaml"},
		{"mixed case overlay dir", "." + strings.ToUpper(strings.TrimPrefix(dir, ".")[:1]) + strings.TrimPrefix(dir, ".")[1:] + "/approvals.yaml"},
		{"dot segment", dir + "/./approvals.yaml"},
		{"nested re-entry", dir + "/sub/../approvals.yaml"},
		{"trailing dot on basename", dir + "/approvals.yaml."},
		{"alternate data stream", dir + "/approvals.yaml::$DATA"},
		{"absolute escape", "/tmp/whatever/approvals.yaml"},
		{"parent traversal", "../../approvals.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			zipPath := filepath.Join(tmpDir, "payload.zip")
			writeTestZip(t, zipPath, map[string]string{tc.entry: "flag: true\n"})

			tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path": "payload.zip",
				"dest": ".",
			}, nativefixture.Context(tmpDir))
			if err == nil {
				t.Fatalf("entry %q extracted without a reject", tc.entry)
			}
			var reject *tools.ToolReject
			if !errors.As(err, &reject) {
				t.Fatalf("entry %q: err = %v, want a structured ToolReject", tc.entry, err)
			}
			t.Logf("entry %q rejected as %s", tc.entry, reject.Code)

			written := filepath.Join(tmpDir, settingsoverlay.DirName(), "approvals.yaml")
			if _, statErr := os.Stat(written); statErr == nil {
				t.Fatalf("entry %q wrote through to the overlay approvals file", tc.entry)
			}
		})
	}
}

func TestExtractEntryLimitPrecedesCommit(t *testing.T) {
	for _, tc := range []struct {
		suffix  string
		write   func(*testing.T, string, map[string]string)
		extract func(extractSink, fseffect.Location, *extractBudget, entryGuard) error
	}{
		{".zip", writeTestZip, extractZipFile},
		{".tar.gz", writeTestTarGz, extractTarGzFile},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			tmpDir := t.TempDir()
			name := "boundary" + tc.suffix
			tc.write(t, filepath.Join(tmpDir, name), map[string]string{"one.txt": "x", "two.txt": "x"})
			tctx := nativefixture.Context(tmpDir)
			tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
			archive, _, err := tool.loadArchive(t.Context(), tctx, name, tctx.ProfileID())
			testutil.FailErr(t, "load archive", err)
			dest, _, err := tool.prepareDest(t.Context(), tctx, "out", tctx.ProfileID())
			testutil.FailErr(t, "prepare destination", err)
			sink := extractSink{ctx: t.Context(), tctx: tctx, dest: resolvedMutationTarget(dest)}
			budget := &extractBudget{entries: hostExtractMaxEntries - 1}
			err = tc.extract(sink, archive.EffectLocation(), budget, protectedEntryGuard)
			var reject *tools.ToolReject
			if !errors.As(err, &reject) || reject.Code != "EXTRACT_ENTRY_LIMIT" {
				t.Fatalf("over-limit extraction: %v", err)
			}
			files, err := os.ReadDir(filepath.Join(tmpDir, "out"))
			testutil.FailErr(t, "read extracted files", err)
			if len(files) != 1 || budget.entries != hostExtractMaxEntries || budget.bytes != 1 || len(budget.out) != 1 {
				t.Fatalf("entry limit committed %d files with budget %+v, want exactly one new entry", len(files), budget)
			}
			got, err := os.ReadFile(filepath.Join(tmpDir, "out", budget.out[0].Path))
			testutil.FailErr(t, "read final allowed entry", err)
			if string(got) != "x" {
				t.Fatalf("final allowed entry = %q, want x", got)
			}
		})
	}
}

func TestExtractSizeLimitPrecedesCommit(t *testing.T) {
	tmpDir := t.TempDir()
	tctx := nativefixture.Context(tmpDir)
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	dest, _, err := tool.prepareDest(context.Background(), tctx, "out", tctx.ProfileID())
	testutil.FailErr(t, "prepare destination", err)
	sink := extractSink{ctx: context.Background(), tctx: tctx, dest: resolvedMutationTarget(dest)}
	existing := filepath.Join(tmpDir, "out", "existing.txt")
	testutil.FailErr(t, "seed existing file", os.WriteFile(existing, []byte("keep"), 0o644))
	budget := &extractBudget{bytes: hostExtractMaxUncompressedBytes - 1}
	_, err = writeExtractFile(sink, sink.target("existing.txt"), strings.NewReader("xx"), budget)
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EXTRACT_SIZE_EXCEEDED" {
		t.Fatalf("oversized entry: %v", err)
	}
	got, err := os.ReadFile(existing)
	testutil.FailErr(t, "read retained file", err)
	if string(got) != "keep" {
		t.Fatalf("oversized entry replaced existing file: %q", got)
	}
	budget.bytes = hostExtractMaxUncompressedBytes
	n, err := writeExtractFile(sink, sink.target("empty.txt"), strings.NewReader(""), budget)
	testutil.FailErr(t, "empty entry at exact byte cap", err)
	if n != 0 {
		t.Fatalf("empty entry bytes: %d", n)
	}
}
