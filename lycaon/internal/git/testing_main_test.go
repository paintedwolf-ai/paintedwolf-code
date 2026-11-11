package git

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/repochange"
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
	// Status cache tests count loads; a watcher event invalidates one mid-load.
	repochange.DisableWatchersForTest()
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
	err := filepath.WalkDir(tmpl.dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(tmpl.dir, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm())
		case mode&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
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
				return err
			}
			return os.WriteFile(target, data, mode.Perm())
		}
	})
	testutil.FailErr(t, "clone fixture tree", err)
}
