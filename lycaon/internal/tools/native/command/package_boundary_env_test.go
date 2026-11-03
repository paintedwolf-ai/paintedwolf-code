package command

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestAgentCommandLaunchInjectsPackageCacheEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	tctx := tools.ToolContext{
		Files: tools.InvocationFiles{PackageExecution: &packageexec.Execution{
			Manager: "bun",
		}},
	}
	confinement := &confine.Confinement{Roots: []string{t.TempDir()}}
	launch := agentCommandLaunch(tctx, "test-command", confinement, nil, "synthetic command")

	if launch.Environment != lycexec.EnvironmentReduced {
		t.Fatalf("expected EnvironmentReduced, got %q", launch.Environment)
	}

	expectedKeys := []string{
		"BUN_INSTALL_CACHE_DIR=",
		"npm_config_cache=",
		"CARGO_HOME=",
		"PIP_CACHE_DIR=",
		"UV_CACHE_DIR=",
		"GOCACHE=",
	}

	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		cacheBase = filepath.Join(home, ".cache")
	}
	for _, key := range expectedKeys {
		found := false
		for _, env := range launch.ExtraEnv {
			if strings.HasPrefix(env, key) {
				found = true
				val := strings.TrimPrefix(env, key)
				if !confine.PathAtOrUnder(fspath.CanonicalPath(val), fspath.CanonicalPath(cacheBase)) {
					t.Fatalf("expected %s to point within %s, got %s", key, cacheBase, val)
				}
				root, err := confine.FilesystemRootForPath("", nil, nil, "", val)
				if err != nil || fspath.CanonicalPath(root) != fspath.CanonicalPath(cacheBase) {
					t.Fatalf("package execution cache %s is outside its actual default boundary: %v", key, err)
				}
				break
			}
		}
		if !found {
			t.Fatalf("expected ExtraEnv to contain %s, got %v", key, launch.ExtraEnv)
		}
	}
}

// Invariant: package command never imports its parent native package.
func TestCommandPackageNeverImportsNative(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read command package: %v", err)
	}
	const forbidden = "github.com/lycaon/lycaon/internal/tools/native"
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		fileName := entry.Name()
		f, err := parser.ParseFile(fset, fileName, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse command source %s: %v", fileName, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == forbidden {
				t.Errorf("%s imports forbidden native package: %s", fileName, path)
			}
			if strings.HasPrefix(path, forbidden+"/") {
				if path != forbidden+"/toolkit" && path != forbidden+"/terminal" && path != forbidden+"/command" {
					t.Errorf("%s imports forbidden native subpackage: %s", fileName, path)
				}
			}
		}
	}
}
