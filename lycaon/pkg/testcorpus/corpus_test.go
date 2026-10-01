package testcorpus_test

import (
	"go/parser"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/testcorpus"
)

func TestCorpusSelectionAndLookup(t *testing.T) {
	root := fixtureTree(t)
	corpus, err := testcorpus.Load(root, testcorpus.Options{Extensions: []string{"go", ".ts"}})
	testutil.FailErr(t, "load corpus", err)

	files := corpus.Files()
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.Rel)
	}
	want := []string{"a.go", "nested/b_test.go", "nested/c.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	file, ok := corpus.File("./nested/b_test.go")
	if !ok || file.Text() != "package nested\n" {
		t.Fatalf("lookup = (%q, %v)", file.Text(), ok)
	}
	under := corpus.Under("nested")
	if len(under) != 2 {
		t.Fatalf("under nested = %d files, want 2", len(under))
	}
}

func TestCorpusUsesDefaultAndExplicitDirectorySkips(t *testing.T) {
	root := fixtureTree(t)
	writeFixture(t, root, "vendor/skipped.go", "package skipped\n")
	writeFixture(t, root, "custom/skipped.go", "package skipped\n")

	defaults, err := testcorpus.Load(root, testcorpus.Options{Extensions: []string{".go"}})
	testutil.FailErr(t, "load default corpus", err)
	if _, ok := defaults.File("vendor/skipped.go"); ok {
		t.Fatal("default corpus included vendor")
	}
	explicit, err := testcorpus.Load(root, testcorpus.Options{
		Extensions:      []string{".go"},
		SkipDirectories: []string{"custom"},
	})
	testutil.FailErr(t, "load explicit corpus", err)
	if _, ok := explicit.File("vendor/skipped.go"); !ok {
		t.Fatal("explicit skip set unexpectedly retained defaults")
	}
	if _, ok := explicit.File("custom/skipped.go"); ok {
		t.Fatal("explicit corpus included custom skip directory")
	}
}

func TestGoCorpusSharesSourceAndPositions(t *testing.T) {
	root := fixtureTree(t)
	corpus, err := testcorpus.LoadGo(root, testcorpus.Options{}, parser.ParseComments|parser.SkipObjectResolution)
	testutil.FailErr(t, "load Go corpus", err)
	if got := len(corpus.Files()); got != 2 {
		t.Fatalf("Go files = %d, want 2", got)
	}
	if got := len(corpus.Production()); got != 1 {
		t.Fatalf("production files = %d, want 1", got)
	}
	if got := len(corpus.Tests()); got != 1 {
		t.Fatalf("test files = %d, want 1", got)
	}
	for _, file := range corpus.Files() {
		if corpus.Fset.Position(file.AST.Pos()).Filename != file.Path {
			t.Fatalf("position filename does not match %s", file.Path)
		}
		if len(file.Bytes()) == 0 {
			t.Fatalf("source bytes missing for %s", file.Rel)
		}
	}
}

func TestLoaderCachesNormalizedOptions(t *testing.T) {
	root := fixtureTree(t)
	var loader testcorpus.Loader
	first, err := loader.Load(root, testcorpus.Options{Extensions: []string{"go", ".ts"}})
	testutil.FailErr(t, "first load", err)
	second, err := loader.Load(root, testcorpus.Options{Extensions: []string{".ts", ".go", ".go"}})
	testutil.FailErr(t, "second load", err)
	if first != second {
		t.Fatal("normalized equivalent options did not share a corpus")
	}
}

func TestGoLoaderCachesParserMode(t *testing.T) {
	root := fixtureTree(t)
	var loader testcorpus.GoLoader
	first, err := loader.Load(root, testcorpus.Options{}, parser.SkipObjectResolution)
	testutil.FailErr(t, "first Go load", err)
	second, err := loader.Load(root, testcorpus.Options{}, parser.SkipObjectResolution)
	testutil.FailErr(t, "second Go load", err)
	if first != second {
		t.Fatal("equivalent Go loads did not share a corpus")
	}
	withComments, err := loader.Load(root, testcorpus.Options{}, parser.ParseComments|parser.SkipObjectResolution)
	testutil.FailErr(t, "Go load with comments", err)
	if first == withComments {
		t.Fatal("different parser modes shared a corpus")
	}
}

func TestLoadRejectsFileRoot(t *testing.T) {
	root := fixtureTree(t)
	if _, err := testcorpus.Load(filepath.Join(root, "a.go"), testcorpus.Options{}); err == nil {
		t.Fatal("Load accepted a file as its root")
	}
}

func TestCorpusIsASnapshot(t *testing.T) {
	root := fixtureTree(t)
	var loader testcorpus.Loader
	corpus, err := loader.Load(root, testcorpus.Options{Extensions: []string{".go"}})
	testutil.FailErr(t, "load snapshot", err)
	writeFixture(t, root, "a.go", "package changed\n")
	writeFixture(t, root, "later.go", "package later\n")

	file, ok := corpus.File("a.go")
	if !ok || file.Text() != "package root\n" {
		t.Fatalf("cached source = (%q, %v), want original snapshot", file.Text(), ok)
	}
	if _, ok := corpus.File("later.go"); ok {
		t.Fatal("cached corpus changed after a file was added")
	}
	bytes := file.Bytes()
	bytes[0] = 'X'
	if file.Text() != "package root\n" {
		t.Fatal("mutating returned bytes changed the cached snapshot")
	}
}

func TestLoaderCoalescesConcurrentLoads(t *testing.T) {
	root := fixtureTree(t)
	var loader testcorpus.Loader
	const workers = 32
	results := make(chan *testcorpus.Corpus, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			corpus, err := loader.Load(root, testcorpus.Options{Extensions: []string{"go", ".ts"}})
			results <- corpus
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent load", err)
	}
	var first *testcorpus.Corpus
	for corpus := range results {
		if first == nil {
			first = corpus
			continue
		}
		if corpus != first {
			t.Fatal("concurrent equivalent loads returned different snapshots")
		}
	}
}

func TestLoadGoReportsTheSourcePath(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "broken.go", "package")
	_, err := testcorpus.LoadGo(root, testcorpus.Options{}, parser.SkipObjectResolution)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(root, "broken.go")) {
		t.Fatalf("parse error = %v, want source path", err)
	}
}

func TestLoadDoesNotFollowSymlinkedFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	writeFixture(t, filepath.Dir(outside), filepath.Base(outside), "package outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	corpus, err := testcorpus.Load(root, testcorpus.Options{Extensions: []string{".go"}})
	testutil.FailErr(t, "load corpus with symlink", err)
	if len(corpus.Files()) != 0 {
		t.Fatalf("symlinked source escaped corpus root: %+v", corpus.Files())
	}
}

func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package root\n")
	writeFixture(t, root, "nested/b_test.go", "package nested\n")
	writeFixture(t, root, "nested/c.ts", "export const c = true\n")
	writeFixture(t, root, "nested/d.txt", "ignored\n")
	return root
}

func writeFixture(t *testing.T, root, rel, data string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "create fixture directory", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		testutil.FailErr(t, "write fixture", err)
	}
}
