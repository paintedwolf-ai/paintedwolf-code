package laws

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/pkg/testcorpus"
)

// Corpus is everything the laws read. Each part loads on first use, once.
// Accessors end the running check on a load error or an empty selection.
type Corpus struct {
	root  string
	goEnv []string

	goOnce  sync.Once
	goSrc   *GoCorpus
	goErr   error
	typed   onceValue[*Program]
	texts   sync.Map // repo-relative path -> *onceValue[[]byte]
	trees   testcorpus.Loader
	parts   sync.Map // part name -> *onceValue[any]
	parses  atomic.Int64
	typings atomic.Int64
	scratch onceValue[string]
}

// Stats counts the expensive loads one Corpus performed.
type Stats struct {
	// GoParses counts full parses of the Go source tree.
	GoParses int64
	// TypeChecks counts type-checks of the Go production packages.
	TypeChecks int64
}

// Load returns a corpus rooted at a repository checkout or a fixture tree
// shaped like one. Nothing is read until a rule asks.
func Load(root string) *Corpus {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Corpus{root: root}
}

// LoadFixture returns a corpus over a fixture tree whose module resolves
// without the repository's workspace or vendor settings.
func LoadFixture(root string) *Corpus {
	c := Load(root)
	c.goEnv = []string{"GOFLAGS=-mod=mod", "GOWORK=off"}
	return c
}

var shared struct {
	once   sync.Once
	corpus *Corpus
	err    error
}

// Shared returns the process-wide corpus of the enclosing repository.
func Shared() (*Corpus, error) {
	shared.once.Do(func() {
		var root string
		root, shared.err = findRepoRoot()
		shared.corpus = Load(root)
	})
	return shared.corpus, shared.err
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "lycaon", "go.mod")); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no repository root above the working directory")
		}
		dir = parent
	}
}

// Root is the absolute repository root.
func (c *Corpus) Root() string { return c.root }

// Stats reports the loads performed so far.
func (c *Corpus) Stats() Stats {
	return Stats{GoParses: c.parses.Load(), TypeChecks: c.typings.Load()}
}

// Scratch returns a directory loaders may write to, such as a user config
// directory a catalog loads against. Close removes it.
func (c *Corpus) Scratch() string {
	dir, err := c.scratch.get(func() (string, error) { return os.MkdirTemp("", "laws-") })
	if err != nil {
		panic(abort{fmt.Errorf("create scratch directory: %w", err)})
	}
	return dir
}

// Close removes the scratch directory, if one was made.
func (c *Corpus) Close() error {
	if c.scratch.value == "" {
		return nil
	}
	return os.RemoveAll(c.scratch.value)
}

// Path joins a repository-relative path onto the root.
func (c *Corpus) Path(rel string) string {
	return filepath.Join(c.root, filepath.FromSlash(rel))
}

type onceValue[T any] struct {
	once  sync.Once
	value T
	err   error
}

// get loads once. A load that ends its check through an accessor fails
// every later get with the same error.
func (o *onceValue[T]) get(load func() (T, error)) (T, error) {
	o.once.Do(func() {
		defer func() {
			if p := recover(); p != nil {
				failure, ok := p.(abort)
				if !ok {
					panic(p)
				}
				o.err = failure.err
			}
		}()
		o.value, o.err = load()
	})
	return o.value, o.err
}

// Part memoizes a derived input, such as a loaded catalog, under name. A
// load error ends the check.
func Part[T any](c *Corpus, name string, load func() (T, error)) T {
	return part(c, name, load)
}

func part[T any](c *Corpus, name string, load func() (T, error)) T {
	raw, _ := c.parts.LoadOrStore(name, &onceValue[any]{})
	value, err := raw.(*onceValue[any]).get(func() (any, error) { return load() })
	if err != nil {
		panic(abort{fmt.Errorf("load %s: %w", name, err)})
	}
	return value.(T)
}

// readFile returns a repository file's bytes, once per process.
func (c *Corpus) readFile(rel string) ([]byte, error) {
	rel = cleanRel(rel)
	raw, _ := c.texts.LoadOrStore(rel, &onceValue[[]byte]{})
	return raw.(*onceValue[[]byte]).get(func() ([]byte, error) {
		data, err := os.ReadFile(c.Path(rel))
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("%s is empty", rel)
		}
		return data, nil
	})
}

// Bytes returns a repository file; a missing or empty file ends the check.
func (c *Corpus) Bytes(rel string) []byte {
	data, err := c.readFile(rel)
	if err != nil {
		panic(abort{fmt.Errorf("read %s: %w", rel, err)})
	}
	return data
}

// Text returns a repository file as a string.
func (c *Corpus) Text(rel string) string { return string(c.Bytes(rel)) }

// Tree returns the files below a repository-relative directory with one of
// exts, in path order. An empty selection ends the check.
func (c *Corpus) Tree(dir string, exts ...string) []testcorpus.File {
	corp, err := c.trees.Load(c.Path(dir), testcorpus.Options{Extensions: exts})
	if err != nil {
		panic(abort{fmt.Errorf("load %s: %w", dir, err)})
	}
	files := corp.Files()
	if len(files) == 0 {
		panic(abort{fmt.Errorf("empty selection: no %s files below %s", strings.Join(exts, ", "), dir)})
	}
	return files
}

// Glob returns repository-relative paths matching a slash pattern relative
// to the root. An empty match ends the check.
func (c *Corpus) Glob(pattern string) []string {
	matches, err := filepath.Glob(c.Path(pattern))
	if err != nil {
		panic(abort{fmt.Errorf("glob %s: %w", pattern, err)})
	}
	if len(matches) == 0 {
		panic(abort{fmt.Errorf("empty selection: nothing matches %s", pattern)})
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		rel, err := filepath.Rel(c.root, match)
		if err != nil {
			panic(abort{err})
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func cleanRel(rel string) string {
	return strings.TrimPrefix(path.Clean(filepath.ToSlash(rel)), "./")
}
