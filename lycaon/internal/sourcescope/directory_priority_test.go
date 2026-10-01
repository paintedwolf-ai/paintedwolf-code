package sourcescope

import (
	"fmt"
	"slices"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryPriorityBoundsIgnoreCacheAndReloadsEvictedRules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".ignore", "root-output/\n")
	writeFile(t, root, "module/.ignore", "nested-output/\n")
	writeFile(t, root, "module/first/.ignore", "local-output/\n")
	scope := New(root, Options{Plane: Plane{DeferIgnored: true}})
	scope.own = scopedstore.New[[]gitignore.Pattern](3)
	if !scope.DeferDir("module/first/local-output", "") {
		t.Fatal("initial directory-local rule did not apply")
	}
	for i := range 10 {
		if scope.DeferDir(fmt.Sprintf("module/dir%d/source", i), "") {
			t.Fatal("ordinary directory was deferred")
		}
		if scope.own.Len() > 3 {
			t.Fatal("ignore cache grew past its directory capacity")
		}
	}
	if _, retained := scope.own.Load("module/first"); retained {
		t.Fatal("cold directory rules were not evicted")
	}
	for _, rel := range []string{"root-output", "module/nested-output", "module/first/local-output"} {
		if !scope.DeferDir(rel, "") {
			t.Errorf("cache eviction lost priority for %q", rel)
		}
	}
}

func TestDirectoryPriorityCoversSupportedLanguages(t *testing.T) {
	data, err := config.Read(config.SourceDirectoryPriority)
	testutil.FailErr(t, "read directory priority", err)
	_, err = parseDirectoryPriority(data)
	testutil.FailErr(t, "validate directory priority", err)
	var catalog directoryPriorityCatalog
	testutil.FailErr(t, "decode directory priority", config.DecodeYAML(data, &catalog))
	supported := filekind.SupportedLanguages()
	covered := make(map[string]bool)
	for _, group := range catalog.Groups {
		for _, language := range group.Languages {
			if !slices.Contains(supported, language) {
				t.Errorf("group %q names unsupported language %q", group.ID, language)
			}
			covered[language] = true
		}
	}
	for _, language := range supported {
		if !covered[language] {
			t.Errorf("supported language %q has no indexing priority group", language)
		}
	}
}

func TestDirectoryPriorityMatchesAtAnyDepthWithoutExcluding(t *testing.T) {
	cfg, err := DefaultConfig()
	testutil.FailErr(t, "load source configuration", err)
	scope := New(t.TempDir(), Options{Plane: cfg.Catalog})
	for _, dir := range []string{
		"node_modules", "target", "build", "vendor", "third_party", "__pycache__", ".venv", ".gradle",
		"bin/Debug", "Bin/Release", "obj", "cmake-build-debug", "bazel-out", "pkg/mod", "Pods",
		"Carthage/Checkouts", ".dart_tool", "_build", "_opam", "renv/library", "lua_modules",
		"local/lib", ".terraform", ".sfdx", ".julia", ".quicklisp", ".codeql", "circuit_js",
		"report_cache", ".git", ".hg", ".svn", "_svn", ".bzr", "_darcs", "CVS", "RCS", "SCCS",
		".fossil-settings", ".pijul", ".jj", ".sl", "$tf", ".plastic", ".history",
	} {
		for _, rel := range []string{dir, "project/nested/" + dir, "project/" + dir + "/child"} {
			if !scope.DeferDir(rel, "") {
				t.Errorf("directory %q should be deferred", rel)
			}
			if _, prune := scope.PruneDir(rel, ""); prune || !scope.AdmitPath(rel, true) || !scope.AdmitFile(rel+"/source.txt", "") {
				t.Errorf("priority excluded %q", rel)
			}
		}
	}
	for _, rel := range []string{".", "src", "lib", "bin", "packages", "deps", "Modules", ".github", ".vscode", "build-scripts", "module/src", "node_modules.go", "renv/source"} {
		if scope.DeferDir(rel, "") {
			t.Errorf("ordinary source directory %q should stay early", rel)
		}
	}
	if len(cfg.Capture.DeferredDirectories) != 0 {
		t.Fatal("indexing priority must not change the capture plane")
	}
}

func TestDirectoryPriorityRejectsMalformedCatalogs(t *testing.T) {
	for _, pattern := range []string{"", "../build", "/build", "!src", "#comment", "build/../src", "build//out", "build/", "[", " build", `build\out`} {
		t.Run(fmt.Sprintf("pattern_%q", pattern), func(t *testing.T) {
			if err := validateDirectoryPattern(pattern); err == nil {
				t.Fatalf("accepted invalid directory pattern %q", pattern)
			}
		})
	}
	for _, data := range []string{
		"version: 2\ngroups: []\n",
		"version: 1\ngroups: []\n",
		"version: 1\nunknown: true\n",
		"version: 1\ngroups: [{id: test, patterns: [build]}]\n",
		"version: 1\ngroups: [{id: test, why: output, patterns: []}]\n",
		"version: 1\ngroups: [{id: test, why: output, patterns: [build]}, {id: test, why: output, patterns: [dist]}]\n",
	} {
		if _, err := parseDirectoryPriority([]byte(data)); err == nil {
			t.Errorf("accepted malformed priority catalog %q", data)
		}
	}
	priority, err := parseDirectoryPriority([]byte("version: 1\ngroups: [{id: test, why: output, patterns: [build, dist, build]}]\n"))
	testutil.FailErr(t, "parse duplicate priority patterns", err)
	if !slices.Equal(priority.Deferred, []string{"build", "dist"}) || !slices.Equal(priority.Collapsed, []string{"build", "dist"}) {
		t.Fatalf("deduplicated patterns = %v, collapsed = %v", priority.Deferred, priority.Collapsed)
	}
}

// A group that declares its directories expandable keeps them in the traversal
// order and out of the collapsed set, including where another group builds into
// the same name.
func TestDirectoryPriorityKeepsExpandableGroupsOutOfTheCollapsedSet(t *testing.T) {
	priority, err := parseDirectoryPriority([]byte(
		"version: 1\ngroups:\n" +
			"  - {id: output, why: build products, patterns: [build, vendor]}\n" +
			"  - {id: committed, why: vendored source, collapse: false, patterns: [vendor, third_party]}\n"))
	testutil.FailErr(t, "parse expandable priority group", err)
	if !slices.Equal(priority.Deferred, []string{"build", "vendor", "third_party"}) {
		t.Fatalf("deferred patterns = %v", priority.Deferred)
	}
	if !slices.Equal(priority.Collapsed, []string{"build"}) {
		t.Fatalf("collapsed patterns = %v, want the expandable names excluded", priority.Collapsed)
	}
}

// The bundled catalog collapses generated output and leaves committed
// dependency trees expandable; an installed copy collapses through ignore rules.
func TestBundledPriorityCollapsesOutputAndExpandsCommittedVendoring(t *testing.T) {
	cfg, err := DefaultConfig()
	testutil.FailErr(t, "load source configuration", err)
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "installed-vendor/\n")
	writeFile(t, root, "vendor/dep.go", "")
	writeFile(t, root, "installed-vendor/dep.go", "")
	scope := New(root, Options{Plane: cfg.Catalog})
	for _, dir := range []string{"node_modules", "build", "dist", ".git", "target"} {
		if !scope.CollapseDir(dir, "") || !scope.DeferDir(dir, "") {
			t.Errorf("generated directory %q should be collapsed and deferred", dir)
		}
	}
	for _, dir := range []string{"vendor", "third_party"} {
		if scope.CollapseDir(dir, "") {
			t.Errorf("committed dependency tree %q should stay expandable", dir)
		}
		if !scope.DeferDir(dir, "") {
			t.Errorf("committed dependency tree %q should still be traversed late", dir)
		}
	}
	if !scope.CollapseDir("installed-vendor", "") {
		t.Error("an ignored dependency tree should collapse")
	}
	for _, dir := range []string{"src", "lib", "."} {
		if scope.CollapseDir(dir, "") {
			t.Errorf("ordinary source directory %q should stay open", dir)
		}
	}
}
