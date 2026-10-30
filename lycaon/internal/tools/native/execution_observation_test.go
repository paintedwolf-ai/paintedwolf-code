package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestExecutionCapturesLearnCompleteLiteralFacts(t *testing.T) {
	for _, kind := range []string{"stream", "opened", "bytes", "decoded-source"} {
		t.Run(kind, func(t *testing.T) {
			f := newPinnedDiskFixture(t, nil)
			content := "NeedleToken\n" + strings.Repeat("tail-token\n", sourceBlockBytes)
			path := filepath.Join(f.dir, "capture.txt")
			testutil.FailErr(t, "write captured file", os.WriteFile(path, []byte(content), 0o644))
			catalog := sourcecatalog.New()
			f.revisions.Literals = catalog.Literals
			resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, path)
			testutil.FailErr(t, "resolve captured file", err)
			switch kind {
			case "stream":
				stream, err := f.revisions.OpenStream(t.Context(), f.tctx, path, 0)
				testutil.FailErr(t, "capture stream", err)
				// Learning is complete before any replay byte is requested.
				testutil.FailErr(t, "close unread stream", stream.Reader.Close())
			case "opened":
				file, err := os.Open(path)
				testutil.FailErr(t, "open capture descriptor", err)
				defer file.Close()
				_, pinned, err := f.revisions.PinOpened(t.Context(), resolved, file)
				testutil.FailErr(t, "pin opened input", err)
				if !pinned {
					t.Fatal("regular input was not pinned")
				}
			case "bytes":
				capture, err := f.revisions.CaptureBytes(t.Context(), f.tctx, path, int64(len(content)))
				testutil.FailErr(t, "capture bytes", err)
				capture.Close()
			case "decoded-source":
				capture, err := f.revisions.CaptureSource(t.Context(), f.tctx, path)
				testutil.FailErr(t, "capture decoded source", err)
				capture.Close()
			}
			assertCapturedLiterals(t, catalog, resolved, "tail-token", "definitely-absent-token")
		})
	}
}

func assertCapturedLiterals(t *testing.T, catalog *sourcecatalog.Catalog, resolved projectpaths.Resolved, present, absent string) {
	t.Helper()
	info, err := os.Stat(resolved.Abs)
	testutil.FailErr(t, "stat captured file", err)
	version := fspath.ObserveFile(info)
	entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}
	if catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf(present), entry, version) {
		t.Fatal("captured token was excluded")
	}
	if pruned := catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf(absent), entry, version); pruned != version.Known {
		t.Fatalf("absent token pruned=%v version=%+v", pruned, version)
	}
}

func TestExecutionCaptureFailureDoesNotPublishLiteralFacts(t *testing.T) {
	for _, kind := range []string{"canceled", "limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newPinnedDiskFixture(t, nil)
			path := filepath.Join(f.dir, "capture.txt")
			testutil.FailErr(t, "write capture fixture", os.WriteFile(path, []byte("complete captured token"), 0o644))
			catalog := sourcecatalog.New()
			f.revisions.Literals = catalog.Literals
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			maximum := int64(3)
			if kind == "canceled" {
				cancel()
				maximum = 1024
			}
			_, err := f.revisions.CaptureBytes(ctx, f.tctx, path, maximum)
			if err == nil {
				t.Fatal("failed capture was accepted")
			}
			resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, path)
			testutil.FailErr(t, "resolve failed capture", err)
			info, err := os.Stat(path)
			testutil.FailErr(t, "stat failed capture", err)
			entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Size: info.Size()}
			if catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf("absent-token"), entry, fspath.ObserveFile(info)) {
				t.Fatal("failed capture published literal facts")
			}
		})
	}
}

func TestExecutionCredentialCaptureDoesNotPublishLiteralFacts(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{".env": "PRIVATE_TOKEN=secret\n"})
	catalog := sourcecatalog.New()
	f.revisions.Literals = catalog.Literals
	stream, err := f.revisions.OpenStream(t.Context(), f.tctx, ".env", 0)
	testutil.FailErr(t, "capture credential input", err)
	testutil.FailErr(t, "close credential input", stream.Reader.Close())
	resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, ".env")
	testutil.FailErr(t, "resolve credential input", err)
	info, err := os.Stat(resolved.Abs)
	testutil.FailErr(t, "stat credential input", err)
	entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Size: info.Size()}
	if stream.ContentLearned || catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf("absent-token"), entry, fspath.ObserveFile(info)) {
		t.Fatal("credential capture published content filters")
	}
}

func TestExecutionLearnedFilterDoesNotWeakenReplayConflict(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"source": "ordinary old content\n"})
	catalog := sourcecatalog.New()
	f.revisions.Literals = catalog.Literals
	stream, err := f.revisions.OpenStream(t.Context(), f.tctx, "source", 0)
	testutil.FailErr(t, "capture original input", err)
	defer stream.Reader.Close()
	path := filepath.Join(f.dir, "source")
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat original input", err)
	testutil.FailErr(t, "rewrite captured input", os.WriteFile(path, []byte("NeedleToken changed!\n"), 0o644))
	testutil.FailErr(t, "restore modification time", os.Chtimes(path, info.ModTime(), info.ModTime()))
	body, err := io.ReadAll(stream.Reader)
	var conflict *ExecutionRevisionConflict
	if len(body) != 0 || !errors.As(err, &conflict) {
		t.Fatalf("replay emitted changed bytes %q: %v", body, err)
	}
	resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, "source")
	testutil.FailErr(t, "resolve rewritten input", err)
	current, err := os.Stat(path)
	testutil.FailErr(t, "stat rewritten input", err)
	entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Size: current.Size()}
	if catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf("NeedleToken"), entry, fspath.ObserveFile(current)) {
		t.Fatal("old learned facts excluded rewritten input")
	}
}

func TestExecutionCaptureMetadataConflictReleasesObservation(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"source": "original content\n"})
	catalog := sourcecatalog.New()
	f.revisions.Literals = catalog.Literals
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: sourceBlockBytes, State: 1024})
	testutil.FailErr(t, "create capture memory", err)
	f.revisions.Memory = memory
	resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, "source")
	testutil.FailErr(t, "resolve capture source", err)
	file, err := os.Open(resolved.Abs)
	testutil.FailErr(t, "open capture source", err)
	defer file.Close()
	before, err := file.Stat()
	testutil.FailErr(t, "stat before metadata change", err)
	testutil.FailErr(t, "change capture metadata", os.Chtimes(resolved.Abs, before.ModTime(), before.ModTime().Add(time.Second)))
	_, err = f.revisions.scanFile(t.Context(), resolved, file, before, true)
	var conflict *ExecutionRevisionConflict
	if !errors.As(err, &conflict) || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("conflict=%v memory=%+v", err, memory.InUse())
	}
	current, err := file.Stat()
	testutil.FailErr(t, "stat after capture conflict", err)
	entry := sourcecatalog.Entry{Path: resolved.ScopeRel, Size: current.Size()}
	if catalog.Literals.CanPruneBytes(resolved.Root.Path, litprefilter.AnyOf("absent-token"), entry, fspath.ObserveFile(current)) {
		t.Fatal("rejected capture published literal facts")
	}
	testutil.FailErr(t, "drain rejected capture observation", catalog.Drain(t.Context()))
}

func TestExecutionSymlinkCaptureKeepsAliasLive(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"source": "target content\n"})
	testutil.FailErr(t, "create source alias", os.Symlink("source", filepath.Join(f.dir, "alias")))
	catalog := sourcecatalog.New()
	f.revisions.Literals = catalog.Literals
	stream, err := f.revisions.OpenStream(t.Context(), f.tctx, "alias", 0)
	testutil.FailErr(t, "capture source alias", err)
	testutil.FailErr(t, "close source alias", stream.Reader.Close())
	info, err := os.Stat(filepath.Join(f.dir, "source"))
	testutil.FailErr(t, "stat alias target", err)
	entry := sourcecatalog.Entry{Path: "alias", Size: info.Size()}
	if stream.ContentLearned || catalog.Literals.CanPruneBytes(f.dir, litprefilter.AnyOf("absent-token"), entry, fspath.ObserveFile(info)) {
		t.Fatal("symlink alias published target facts as a regular file")
	}
	testutil.FailErr(t, "drain alias observation", catalog.Drain(t.Context()))
}
