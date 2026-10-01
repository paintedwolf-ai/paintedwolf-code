// Package configtest stages bundled config for a test. It is separate from
// config, which imports nothing from the tree.
package configtest

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/config"
)

// Use replaces the bundled config source for one test and restores it after.
// Bundled config is not read from the host, so a modified default is staged
// here rather than written to disk.
func Use(t *testing.T, fsys fs.FS) {
	t.Helper()
	// The swap is process-wide, so a parallel caller stages its tree under every
	// other running test. t.Setenv makes the runtime reject that caller.
	t.Setenv(stagingGuardEnv, "1")
	t.Cleanup(config.UseFS(fsys))
}

// stagingGuardEnv is set only to borrow t.Setenv's parallel check.
const stagingGuardEnv = "LYCAON_CONFIG_STAGED"

// Overlay stages files on top of the real bundled tree, keyed by config-relative
// path. Everything not named keeps shipping its real default, so a test can
// change one file without reconstructing the catalog.
func Overlay(t *testing.T, files map[config.Rel]string) {
	t.Helper()
	over := fstest.MapFS{}
	for rel, body := range files {
		over[rel.String()] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	Use(t, layered{over: over, base: config.Source()})
}

type layered struct {
	over fs.FS
	base fs.FS
}

func (l layered) Open(name string) (fs.File, error) {
	if f, err := l.over.Open(name); err == nil {
		return f, nil
	}
	return l.base.Open(name)
}

func (l layered) ReadDir(name string) ([]fs.DirEntry, error) {
	overEnts, overErr := fs.ReadDir(l.over, name)
	baseEnts, baseErr := fs.ReadDir(l.base, name)
	if overErr != nil {
		return baseEnts, baseErr
	}
	seen := map[string]bool{}
	out := make([]fs.DirEntry, 0, len(overEnts)+len(baseEnts))
	for _, e := range overEnts {
		seen[e.Name()] = true
		out = append(out, e)
	}
	if baseErr == nil {
		for _, e := range baseEnts {
			if !seen[e.Name()] {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (l layered) Stat(name string) (fs.FileInfo, error) {
	if fi, err := fs.Stat(l.over, name); err == nil {
		return fi, nil
	}
	return fs.Stat(l.base, name)
}

// Only stages an exact tree, with nothing behind it. Use when the test asserts
// on absence — a loader failing closed on an empty catalog, say — where
// inheriting real defaults would hide the case under test.
func Only(t *testing.T, files map[config.Rel]string) {
	t.Helper()
	only := fstest.MapFS{}
	for rel, body := range files {
		only[rel.String()] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	Use(t, only)
}
