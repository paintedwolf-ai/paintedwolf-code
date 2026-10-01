package config

import (
	"path"
	"strings"
)

// Rel is a path inside the bundled config tree, e.g.
// "packs/painted-wolf/platform/host/providers.yaml".
//
// A distinct type, so a host path cannot reach a bundled read at all:
// filepath.Join of a Rel does not compile.
type Rel string

// fsPath renders a Rel for io/fs, which wants slash-separated and unrooted.
func (r Rel) fsPath() string {
	p := path.Clean(strings.TrimPrefix(strings.TrimSpace(string(r)), "/"))
	if p == "" || p == "." {
		return "."
	}
	return p
}

// String renders a Rel for messages and ids. It is not a host path; config.Read
// resolves a Rel.
func (r Rel) String() string { return r.fsPath() }

// Join extends a Rel with further segments.
func (r Rel) Join(parts ...string) Rel {
	if len(parts) == 0 {
		return r
	}
	return Rel(path.Join(append([]string{r.fsPath()}, parts...)...))
}

// Dir is the parent of r.
func (r Rel) Dir() Rel { return Rel(path.Dir(r.fsPath())) }

// Base is the final segment of r.
func (r Rel) Base() string { return path.Base(r.fsPath()) }

// TrimSuffix drops a trailing extension, for turning a unit file into a unit id.
func (r Rel) TrimSuffix(suffix string) Rel {
	return Rel(strings.TrimSuffix(r.fsPath(), suffix))
}
