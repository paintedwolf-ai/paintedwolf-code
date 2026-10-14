// Package config exposes bundled configuration through relative paths.
package config

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

// Reserved underscore-prefixed entries are embedded explicitly.
//
//go:embed packs fixtures/mock_llm.yaml runtime gitengine
//go:embed packs/painted-wolf/implement/agents/prompts/_persona-contract.yaml
//go:embed packs/painted-wolf/plan/agents/prompts/_persona-contract.yaml
//go:embed packs/painted-wolf/platform/agents/prompts/_persona-contract.yaml
//go:embed packs/painted-wolf/platform/agents/prompts/_shell.md
//go:embed packs/painted-wolf/platform/approvals/_unknown
//go:embed packs/painted-wolf/platform/guidance/gate-feedback-templates/_gate.md.tmpl
//go:embed packs/painted-wolf/platform/guidance/reject/_reject.md
//go:embed packs/painted-wolf/platform/workflows/_templates
//go:embed packs/painted-wolf/platform/workflows/_topologies
//go:embed packs/painted-wolf/recon-pack/agents/prompts/_persona-contract.yaml
//go:embed packs/painted-wolf/security-survey/agents/prompts/_persona-contract.yaml
//go:embed packs/painted-wolf/web-research/agents/prompts/_persona-contract.yaml
var embedded embed.FS

// EnvConfigRoot redirects bundled reads to a checkout.
const EnvConfigRoot = "LYCAON_CONFIG_ROOT"

// ConfigDirName is the config subdirectory inside a LYCAON_CONFIG_ROOT checkout.
const ConfigDirName = "config"

// source holds the active bundled configuration tree. Test staging swaps it
// while engine goroutines read it, so every access is atomic.
var source atomic.Pointer[sourceTree]

type sourceTree struct{ fsys fs.FS }

func init() {
	var tree fs.FS = embedded
	if root := strings.TrimSpace(os.Getenv(EnvConfigRoot)); root != "" {
		tree = overlay{disk: os.DirFS(filepath.Join(root, ConfigDirName)), base: embedded}
	}
	source.Store(&sourceTree{fsys: tree})
}

// Source returns the active bundled configuration filesystem.
func Source() fs.FS { return source.Load().fsys }

// SourceImmutable reports whether reads use only the embedded, immutable bytes.
// Disk overlays and caller-supplied filesystems can change without a source swap.
func SourceImmutable() bool {
	_, embeddedOnly := Source().(embed.FS)
	return embeddedOnly
}

// sourceGen tracks source replacements for cache invalidation.
var sourceGen atomic.Uint64

// SourceGeneration identifies the current bundled-source swap state.
func SourceGeneration() uint64 { return sourceGen.Load() }

// UseFS replaces the bundled source until its restore function runs.
func UseFS(fsys fs.FS) func() {
	prev := source.Swap(&sourceTree{fsys: fsys})
	sourceGen.Add(1)
	return func() {
		source.Store(prev)
		sourceGen.Add(1)
	}
}

// overlay prefers disk files and falls back to embedded files.
type overlay struct {
	disk fs.FS
	base fs.FS
}

func (o overlay) Open(name string) (fs.File, error) {
	if f, err := o.disk.Open(name); err == nil {
		return f, nil
	}
	return o.base.Open(name)
}

// ReadDir treats an existing disk directory as authoritative.
func (o overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	if ents, err := fs.ReadDir(o.disk, name); err == nil {
		return ents, nil
	}
	return fs.ReadDir(o.base, name)
}

func (o overlay) Stat(name string) (fs.FileInfo, error) {
	if fi, err := fs.Stat(o.disk, name); err == nil {
		return fi, nil
	}
	return fs.Stat(o.base, name)
}

// OverlayDirToken marks project-overlay paths in bundled configuration.
// #nosec G101 -- This is a template placeholder, not credential material.
const OverlayDirToken = "${overlay_dir}"

// Read returns the bundled file at rel, with OverlayDirToken expanded.
func Read(rel Rel) ([]byte, error) {
	data, err := fs.ReadFile(Source(), rel.fsPath())
	if err != nil {
		return nil, err
	}
	return ExpandOverlayDir(data), nil
}

// DecodeYAML rejects unknown fields and multiple documents.
func DecodeYAML(data []byte, target any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple YAML documents are not allowed")
		}
		return err
	}
	return nil
}

// ExpandOverlayDir substitutes the overlay-directory placeholder.
func ExpandOverlayDir(data []byte) []byte {
	if !bytes.Contains(data, []byte(OverlayDirToken)) {
		return data
	}
	return bytes.ReplaceAll(data, []byte(OverlayDirToken), []byte(settingsoverlay.DirName()))
}

// List returns the entries of the bundled directory at rel, sorted by name.
func List(rel Rel) ([]fs.DirEntry, error) {
	ents, err := fs.ReadDir(Source(), rel.fsPath())
	if err != nil {
		return nil, err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, nil
}

// Info stats the bundled entry at rel.
func Info(rel Rel) (fs.FileInfo, error) { return fs.Stat(Source(), rel.fsPath()) }

// Has reports whether rel exists in the bundled tree.
func Has(rel Rel) bool {
	_, err := Info(rel)
	return err == nil
}

// Walk visits the bundled tree with paths relative to its root.
func Walk(rel Rel, fn func(Rel, fs.DirEntry, error) error) error {
	root := rel.fsPath()
	return fs.WalkDir(Source(), root, func(p string, d fs.DirEntry, err error) error {
		return fn(relFromFS(root, rel, p), d, err)
	})
}

// Sub returns the bundled subtree rooted at rel.
func Sub(rel Rel) (fs.FS, error) { return fs.Sub(Source(), rel.fsPath()) }

func relFromFS(root string, rootRel Rel, p string) Rel {
	suffix := strings.TrimPrefix(p, root)
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		return rootRel
	}
	return rootRel.Join(strings.Split(suffix, "/")...)
}
