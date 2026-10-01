// Package configlayout locates host payloads, schemas, and module roots.
package configlayout

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Module markers identify a checkout.
var moduleMarkers = []string{
	moduleRelative(config.PlatformManifest),
	moduleRelative(config.Providers),
}

func moduleRelative(rel config.Rel) string {
	return filepath.Join(config.ConfigDirName, filepath.FromSlash(rel.String()))
}

// EnvEngineRoot names the staged engine payload directory.
const EnvEngineRoot = "LYCAON_ENGINE_ROOT"

// EngineRoot returns the explicit payload root or the installed host's payloads.
func EngineRoot() string {
	if root := strings.TrimSpace(os.Getenv(EnvEngineRoot)); root != "" {
		return root
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return engineRootForExecutable(executable, runtime.GOOS)
}

// BundledOnlyRoot marks a build without a module root on disk.
const BundledOnlyRoot = "lycaon"

// FindModuleRootFrom locates an absolute module root above start.
func FindModuleRootFrom(start string) string {
	return walkForModuleRoot(filepath.Clean(strings.TrimSpace(start)))
}

// FindModuleRoot locates the module root from process paths.
func FindModuleRoot() string {
	if wd, err := os.Getwd(); err == nil {
		if root := walkForModuleRoot(wd); root != "" {
			return root
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root := walkForModuleRoot(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	// A repository-root working directory holds the module one level down.
	if IsModuleRoot(BundledOnlyRoot) {
		return absoluteOrEmpty(BundledOnlyRoot)
	}
	return BundledOnlyRoot
}

func walkForModuleRoot(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if IsModuleRoot(dir) {
			return absoluteOrEmpty(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

// absoluteOrEmpty resolves dir or reports absence.
func absoluteOrEmpty(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return abs
}

// IsModuleRoot reports whether dir contains module markers.
func IsModuleRoot(dir string) bool {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return false
	}
	for _, marker := range moduleMarkers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// SchemasDirName is the staged schema tree.
const SchemasDirName = "schemas"

// schemasDirProbe distinguishes a populated schema tree.
const schemasDirProbe = "oar/oar.schema.json"

// SchemasDir locates the schema tree.
func SchemasDir(moduleRoot string) string {
	var candidates []string
	if root := EngineRoot(); root != "" {
		candidates = append(candidates, filepath.Join(root, SchemasDirName))
	}
	if moduleRoot = filepath.Clean(strings.TrimSpace(moduleRoot)); moduleRoot != "" && moduleRoot != "." {
		candidates = append(candidates, filepath.Join(filepath.Dir(moduleRoot), SchemasDirName))
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, schemasDirProbe)); err == nil {
			return dir
		}
	}
	return ""
}

// allowedConfigRootEntries are the top-level names permitted under lycaon/config/.
// The Go files are the package that embeds the tree; everything else is content.
var allowedConfigRootEntries = []string{
	"README.md",
	"config.go",
	"config_test.go",
	"configtest",
	"fixtures",
	"gitengine",
	"oar-portable-pack",
	"packs",
	"paths.go",
	"rel.go",
	"runtime",
}

// AllowedConfigRootEntries returns the permitted top-level names.
func AllowedConfigRootEntries() []string {
	return append([]string(nil), allowedConfigRootEntries...)
}
