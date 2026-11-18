package git

import (
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"

	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/gitengine"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
)

var (
	templateRoot string
	templatesMu  sync.Mutex
	templates    = map[string]*fixtureTemplate{}
)

type fixtureTemplate struct {
	once sync.Once
	dir  string
}

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	// Without the pinned git every repository test fails on its own; say why once.
	if _, err := gitengine.BinaryPath(); err != nil {
		fmt.Fprintf(os.Stderr, "bundled git is not staged; run ./task gitengine:fetch: %v\n", err)
		os.Exit(1)
	}
	releaseWatchers := repochangetest.Suite()
	// Detached auto-maintenance rewrites shared templates while tests clone them.
	for key, value := range map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "maintenance.auto",
		"GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1":   "gc.autoDetach",
		"GIT_CONFIG_VALUE_1": "false",
	} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	root, err := os.MkdirTemp("", "git-templates-*")
	if err != nil {
		panic(err)
	}
	templateRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		_ = os.RemoveAll(root)
		panic(err)
	}
	code := m.Run()
	releaseWatchers()
	_ = os.RemoveAll(templateRoot)
	os.Exit(code)
}

// CloneFixtureRepo seeds name once and clones its directory tree into a fresh TempDir.
func CloneFixtureRepo(t *testing.T, name string, seed func(dir string)) string {
	t.Helper()
	dst := t.TempDir()
	CloneFixtureInto(t, name, dst, seed)
	return dst
}

// CloneFixtureInto seeds name once and clones its directory tree into dst.
func CloneFixtureInto(t *testing.T, name, dst string, seed func(dir string)) {
	t.Helper()
	templatesMu.Lock()
	tmpl := templates[name]
	if tmpl == nil {
		tmpl = &fixtureTemplate{dir: filepath.Join(templateRoot, name)}
		templates[name] = tmpl
	}
	templatesMu.Unlock()
	tmpl.once.Do(func() {
		testutil.FailErr(t, "mkdir fixture template", os.MkdirAll(tmpl.dir, 0o755))
		seed(tmpl.dir)
	})
	// A seed's detached git maintenance can remove files (e.g.
	// objects/maintenance.lock) while this walk is in flight, so an entry that
	// vanishes between readdir and its stat is skipped. A missing template
	// root and every other error stay fatal.
	err := filepath.WalkDir(tmpl.dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == tmpl.dir || !errors.Is(walkErr, fs.ErrNotExist) {
				return walkErr
			}
			return nil
		}
		rel, err := filepath.Rel(tmpl.dir, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm())
		case mode&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			return os.Symlink(link, target)
		default:
			if cloned, err := fileclone.Clone(path, target); err == nil && cloned {
				if runtime.GOOS != "darwin" && mode.Perm() != 0o600 {
					return os.Chmod(target, mode.Perm())
				}
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			return os.WriteFile(target, data, mode.Perm())
		}
	})
	testutil.FailErr(t, "clone fixture tree", err)
}

// SeedGitEnv disables detached Git maintenance while fixture templates are built.
func SeedGitEnv(extra ...string) []string {
	env := append(lyexec.InheritedEnviron(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=maintenance.auto",
		"GIT_CONFIG_VALUE_0=false",
	)
	return append(env, extra...)
}
