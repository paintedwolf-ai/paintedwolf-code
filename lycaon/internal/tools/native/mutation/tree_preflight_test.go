package mutation

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestArchivePreflightIncludesHiddenMetadataLinks(t *testing.T) {
	for _, directory := range []string{".hidden", ".git", ".paintedwolf"} {
		t.Run(directory, func(t *testing.T) {
			h := newHarness(t, map[string]string{"src/target": "data", "src/" + directory + "/keep": "data"})
			if err := os.Symlink("../target", filepath.Join(h.root, "src", directory, "link")); err != nil {
				testutil.FailErr(t, "create metadata fixture symlink", err)
			}
			result := h.run("", "cp", "-a", "src", "copy")
			reject := toolrejection.AsToolReject(result.err)
			if reject == nil || reject.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
				t.Fatalf("archive result %+v err=%v", result.result, result.err)
			}
			if _, err := os.Lstat(filepath.Join(h.root, "copy")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("archive published before refusal: %v", err)
			}
		})
	}
}

func TestMutationTreePreflightCountsRootAndPreservesReadPolicy(t *testing.T) {
	h := newHarness(t, map[string]string{"single": "data", "tree/gone/child": "data", "tree/kept": "data"})
	setLimit := func(maximum int64) {
		for i := range h.owners.catalog.Language.Limits {
			if h.owners.catalog.Language.Limits[i].ID == "recursive_mutation_files" {
				h.owners.catalog.Language.Limits[i].Maximum = maximum
				return
			}
		}
		t.Fatal("missing limit")
	}
	inv := &invocation{ctx: t.Context(), owners: h.owners}
	setLimit(1)
	count := 0
	visit := func(sandbox.SurveyEntry) error { count++; return nil }
	if err := inv.visitTree(filepath.Join(h.root, "single"), nil, visit); err != nil || count != 1 {
		t.Fatalf("single count=%d err=%v", count, err)
	}
	setLimit(0)
	reject := toolrejection.AsToolReject(inv.visitTree(filepath.Join(h.root, "single"), nil, visit))
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["observed"] != int64(1) {
		t.Fatalf("root count refusal %+v", reject)
	}
	setLimit(100)
	kept := false
	err := inv.visitTree(filepath.Join(h.root, "tree"), missingEntry, func(entry sandbox.SurveyEntry) error {
		if entry.Rel == "gone" {
			return os.RemoveAll(entry.Abs)
		}
		if entry.Rel == "kept" {
			kept = true
		}
		return nil
	})
	if err != nil || !kept {
		t.Fatalf("missing descendant stopped siblings: kept=%v err=%v", kept, err)
	}
	missing := filepath.Join(h.root, "missing")
	if err := inv.visitTree(missing, missingEntry, visit); err != nil {
		testutil.FailErr(t, "visit missing tree entry", err)
	}
	if err := inv.visitTree(missing, nil, visit); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("strict root %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inv.ctx = ctx
	if err := inv.visitTree(missing, missingEntry, visit); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}

func TestVerboseRemovalSurveysMetadataDirectories(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/.hidden/file": "a", "tree/.git/config": "b", "tree/.paintedwolf/settings": "c"})
	argv, err := (commandprogram.CatalogBinder{}).Bind(h.owners.specs["rm"], commandprogram.Fields{{Value: "rm"}, {Value: "-v"}, {Value: "tree"}})
	if err != nil {
		t.Fatal(err)
	}
	inv := &invocation{ctx: t.Context(), owners: h.owners, argv: argv}
	dirs, err := inv.directoriesUnder(filepath.Join(h.root, "tree"))
	testutil.FailErr(t, "inventory removal directories", err)
	for _, rel := range []string{"tree", "tree/.hidden", "tree/.git", "tree/.paintedwolf"} {
		if !dirs[filepath.Join(h.root, rel)] {
			t.Fatalf("directory missing from preflight: %s", rel)
		}
	}
	result := h.run("", "rm", "-r", "-v", "tree")
	reject := toolrejection.AsToolReject(result.err)
	if reject == nil || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("metadata confinement lost: %+v %v", result.result, result.err)
	}
	if _, err := os.Lstat(filepath.Join(h.root, "tree/.git/config")); err != nil {
		t.Fatalf("refused removal changed tree: %v", err)
	}
	admissible := newHarness(t, map[string]string{"tree/.hidden/file": "a"})
	result = admissible.run("", "rm", "-r", "-v", "tree")
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("remove %+v err=%v stderr=%s", result.result, result.err, result.stderr)
	}
	for _, rel := range []string{"tree", "tree/.hidden"} {
		if !strings.Contains(result.stdout, "removed directory '"+rel+"'\n") {
			t.Fatalf("directory classification: %q", result.stdout)
		}
	}
	if _, err := os.Lstat(filepath.Join(admissible.root, "tree")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("tree remains: %v", err)
	}
}
