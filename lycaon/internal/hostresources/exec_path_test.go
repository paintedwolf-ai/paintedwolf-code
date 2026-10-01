package hostresources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// vendorBundle builds the shape this feature exists for: a real binary inside an
// application bundle, its helpers beside it, and a symlink on PATH pointing at it.
func vendorBundle(t *testing.T, names ...string) (linkDir, realDir string) {
	t.Helper()
	root := t.TempDir()
	realDir = filepath.Join(root, "Vendor.app", "Contents", "Resources", "bin")
	linkDir = filepath.Join(root, "usr", "local", "bin")
	testutil.FailErr(t, "mkdir real", os.MkdirAll(realDir, 0o755))
	testutil.FailErr(t, "mkdir link", os.MkdirAll(linkDir, 0o755))
	for _, name := range names {
		target := filepath.Join(realDir, name)
		testutil.FailErr(t, "write "+name, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
		testutil.FailErr(t, "link "+name, os.Symlink(target, filepath.Join(linkDir, name)))
	}
	return linkDir, realDir
}

func serviceForPath(t *testing.T, def Definition, searchDirs []string) *Service {
	t.Helper()
	service := &Service{
		defs:              []Definition{def},
		env:               discoveryEnvironment{platform: "macos", stat: os.Stat},
		ttl:               time.Minute,
		now:               func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) },
		connectionSupport: func(string, Connection) bool { return true },
	}
	service.env.lookPath = lookPathIn(searchDirs, os.Stat)
	return service
}

func execDef(id string, names ...string) Definition {
	return Definition{
		ID: id, Family: "test." + id, Label: id, Category: "Test", Description: id + ".",
		Surfaces:     []ExecutionSurface{SurfaceProcessExec},
		Realizations: []Realization{{Discover: Expression{Executable: &ExecutableProbe{Names: names}}}},
		origin:       "test",
	}
}

// Helpers live beside the resolved binary and on no PATH at all.
func TestPathExtraResolvesThroughTheSymlink(t *testing.T) {
	linkDir, realDir := vendorBundle(t, "vtool", "vtool-credential-helper")
	service := serviceForPath(t, execDef("vendor", "vtool"), []string{linkDir})

	resolution, unmet := service.ResolveAction(
		context.Background(), []string{"vendor"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
	)
	if len(unmet) != 0 {
		t.Fatalf("unmet = %v", unmet)
	}
	wantDir, err := filepath.EvalSymlinks(realDir)
	testutil.FailErr(t, "EvalSymlinks", err)
	if len(resolution.PathExtra) != 1 || resolution.PathExtra[0] != wantDir {
		t.Fatalf("PathExtra = %v, want the resolved bundle directory %q", resolution.PathExtra, wantDir)
	}
	if _, err := os.Stat(filepath.Join(resolution.PathExtra[0], "vtool-credential-helper")); err != nil {
		t.Fatalf("the helper this exists for is not in the contributed directory: %v", err)
	}
}

// A probe naming several candidates has to keep looking past the ones that are absent.
func TestPathExtraTriesEveryDeclaredName(t *testing.T) {
	linkDir, realDir := vendorBundle(t, "second")
	service := serviceForPath(t, execDef("vendor", "first", "second"), []string{linkDir})

	resolution, _ := service.ResolveAction(
		context.Background(), []string{"vendor"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
	)
	wantDir, err := filepath.EvalSymlinks(realDir)
	testutil.FailErr(t, "EvalSymlinks", err)
	if len(resolution.PathExtra) != 1 || resolution.PathExtra[0] != wantDir {
		t.Fatalf("PathExtra = %v, want %q found under the second declared name", resolution.PathExtra, wantDir)
	}
}

// Two resources shipped by one vendor resolve into the same directory. Contributing it
// twice would grow a child's PATH for no reason.
func TestPathExtraDeduplicatesSharedDirectories(t *testing.T) {
	linkDir, _ := vendorBundle(t, "alpha", "beta")
	service := serviceForPath(t, execDef("alpha", "alpha"), []string{linkDir})
	service.defs = append(service.defs, execDef("beta", "beta"))

	resolution, _ := service.ResolveAction(
		context.Background(), []string{"alpha", "beta"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
	)
	if len(resolution.PathExtra) != 1 {
		t.Fatalf("PathExtra = %v, want one entry for one shared directory", resolution.PathExtra)
	}
}

// Order is the caller's, because when two requested resources ship a program of the same
// name the one asked for first is the one they meant.
func TestPathExtraFollowsDeclaredOrder(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	for dir, name := range map[string]string{rootA: "alpha", rootB: "beta"} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755))
	}
	service := serviceForPath(t, execDef("alpha", "alpha"), []string{rootA, rootB})
	service.defs = append(service.defs, execDef("beta", "beta"))

	resolvedA, err := filepath.EvalSymlinks(rootA)
	testutil.FailErr(t, "EvalSymlinks A", err)
	resolvedB, err := filepath.EvalSymlinks(rootB)
	testutil.FailErr(t, "EvalSymlinks B", err)

	forward, _ := service.ResolveAction(
		context.Background(), []string{"beta", "alpha"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
	)
	if strings.Join(forward.PathExtra, ":") != resolvedB+":"+resolvedA {
		t.Fatalf("PathExtra = %v, want declared order beta then alpha", forward.PathExtra)
	}
}

// Fail closed: a name that cannot be canonicalized contributes nothing rather than a
// directory the app cannot describe.
func TestPathExtraContributesNothingForUnresolvableTools(t *testing.T) {
	cases := map[string]func(t *testing.T) (Definition, []string){
		"executable is absent": func(t *testing.T) (Definition, []string) {
			return execDef("vendor", "missing"), []string{t.TempDir()}
		},
		"symlink dangles": func(t *testing.T) (Definition, []string) {
			dir := t.TempDir()
			testutil.FailErr(t, "link", os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "vtool")))
			return execDef("vendor", "vtool"), []string{dir}
		},
		"probe names no executable": func(t *testing.T) (Definition, []string) {
			def := execDef("vendor")
			def.Realizations[0].Discover = Expression{Path: &PathProbe{Paths: []string{"/nonexistent"}, Kind: "socket"}}
			return def, []string{t.TempDir()}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			def, dirs := build(t)
			service := serviceForPath(t, def, dirs)
			// Availability is beside the point: the assertion is that nothing
			// half-resolved reaches a child's PATH.
			resolution, _ := service.ResolveAction(
				context.Background(), []string{"vendor"}, ProjectContext{}, []ExecutionSurface{SurfaceProcessExec},
			)
			if len(resolution.PathExtra) != 0 {
				t.Fatalf("PathExtra = %v, want nothing contributed", resolution.PathExtra)
			}
		})
	}
}

// A resource the person was never granted must not put its install directory on PATH.
func TestPathExtraSkipsUnresolvedResources(t *testing.T) {
	linkDir, _ := vendorBundle(t, "vtool")
	service := serviceForPath(t, execDef("vendor", "vtool"), []string{linkDir})

	resolution, unmet := service.ResolveAction(
		context.Background(), []string{"vendor"}, ProjectContext{}, []ExecutionSurface{"nonexistent_surface"},
	)
	if len(unmet) != 1 {
		t.Fatalf("unmet = %v, want the resource unmet for an unsupported surface", unmet)
	}
	if len(resolution.PathExtra) != 0 {
		t.Fatalf("PathExtra = %v, want nothing for a resource that was not resolved", resolution.PathExtra)
	}
}

// Discovery and execution have to search the same PATH, or a resource can be reported
// missing and then work when invoked.
func TestResolvedPathDrivesDiscovery(t *testing.T) {
	linkDir, _ := vendorBundle(t, "vtool")
	t.Setenv("PATH", t.TempDir())
	resolved := ""
	exec.SetResolvedPathSource(func() string { return resolved })
	t.Cleanup(func() { exec.SetResolvedPathSource(nil) })
	service := &Service{
		defs:              []Definition{execDef("vendor", "vtool")},
		env:               discoveryEnvironment{platform: "macos", stat: os.Stat, lookPath: resolvedLookPath},
		ttl:               time.Minute,
		now:               func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) },
		connectionSupport: func(string, Connection) bool { return true },
	}
	service.cache = catalogruntime.NewSnapshotCacheWithClock(service.ttl, cloneSnapshot, service.now)

	if status := service.Snapshot(context.Background(), false).Resources[0].Status; status != StatusUnavailable {
		t.Fatalf("status = %q, want unavailable on the process PATH alone", status)
	}
	resolved = linkDir
	service.cache.Invalidate()
	if status := service.Snapshot(context.Background(), false).Resources[0].Status; status != StatusAvailable {
		t.Fatalf("status = %q, want available once discovery searches the resolved PATH", status)
	}
}
