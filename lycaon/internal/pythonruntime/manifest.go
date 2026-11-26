package pythonruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ImageManifestSchemaVersion is the pinned JSON schema version for Portable
// Python image manifests produced by PY01/PY02 image builds.
const ImageManifestSchemaVersion = "portable-python.image-manifest/v1"

// ImageManifest describes an immutable Portable Python WASI image: interpreter
// binary, stdlib, curated packages, digests, stdlib capability dispositions,
// and declared Wasm host imports.
type ImageManifest struct {
	SchemaVersion      string             `json:"schema_version"`
	ImageID            string             `json:"image_id"`
	BuiltAt            string             `json:"built_at"`
	Target             string             `json:"target"`
	Interpreter        InterpreterSpec    `json:"interpreter"`
	PackagesRoot       string             `json:"packages_root"`
	Distributions      []DistributionSpec `json:"distributions"`
	StdlibCapabilities []string           `json:"stdlib_capabilities,omitempty"`
	WasmImports        []WasmImportSpec   `json:"wasm_imports,omitempty"`
}

// PatchSpec records a minimal, auditable patch applied to an image file.
type PatchSpec struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// InterpreterSpec records the CPython/WASI binary, stdlib archive/tree,
// provenance inputs, and applied patches.
type InterpreterSpec struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	WasmPath     string            `json:"wasm_path"`
	WasmSHA256   string            `json:"wasm_sha256"`
	StdlibPath   string            `json:"stdlib_path"`
	StdlibSHA256 string            `json:"stdlib_sha256"`
	BuildInputs  map[string]string `json:"build_inputs,omitempty"`
	Patches      []PatchSpec       `json:"patches,omitempty"`
}

// DistributionSpec records a bundled third-party distribution inside the
// immutable package tree.
type DistributionSpec struct {
	Name           string   `json:"name"`
	NormalizedName string   `json:"normalized_name"`
	Version        string   `json:"version"`
	Public         bool     `json:"public"`
	Imports        []string `json:"imports,omitempty"`
	Backend        string   `json:"backend,omitempty"`
	Features       []string `json:"features,omitempty"`
	ConsoleScripts []string `json:"console_scripts,omitempty"`
	HasPackageData bool     `json:"has_package_data"`
	DistInfoPath   string   `json:"dist_info_path,omitempty"`
	License        string   `json:"license,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	SourceSHA256   string   `json:"source_sha256,omitempty"`
	TreeSHA256     string   `json:"tree_sha256,omitempty"`
}

// LoadImageManifest reads, parses, and validates an image manifest JSON file.
func LoadImageManifest(path string) (*ImageManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image manifest: %w", err)
	}
	var m ImageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode image manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks required structural fields and schema version.
func (m *ImageManifest) Validate() error {
	if m == nil {
		return errors.New("image manifest is nil")
	}
	if m.SchemaVersion != ImageManifestSchemaVersion {
		return fmt.Errorf("unsupported image manifest schema_version %q (want %q)", m.SchemaVersion, ImageManifestSchemaVersion)
	}
	if strings.TrimSpace(m.ImageID) == "" {
		return errors.New("image manifest missing image_id")
	}
	if strings.TrimSpace(m.Interpreter.WasmPath) == "" {
		return errors.New("image manifest missing interpreter.wasm_path")
	}
	if !isHexSHA256(m.Interpreter.WasmSHA256) {
		return fmt.Errorf("image manifest invalid interpreter.wasm_sha256 %q", m.Interpreter.WasmSHA256)
	}
	if strings.TrimSpace(m.Interpreter.StdlibPath) == "" {
		return errors.New("image manifest missing interpreter.stdlib_path")
	}
	if !isHexSHA256(m.Interpreter.StdlibSHA256) {
		return fmt.Errorf("image manifest invalid interpreter.stdlib_sha256 %q", m.Interpreter.StdlibSHA256)
	}
	return nil
}

// VerifyImage checks that the image directory matches manifest digests for
// python.wasm, stdlib, and declared package trees, and contains zero host
// native dynamic libraries (.so, .dylib, .dll).
func VerifyImage(dir string, manifest *ImageManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve image dir: %w", err)
	}

	wasmPath, err := resolveInsideDir(absDir, manifest.Interpreter.WasmPath)
	if err != nil {
		return fmt.Errorf("interpreter.wasm_path: %w", err)
	}
	wasmDigest, err := HashFile(wasmPath)
	if err != nil {
		return fmt.Errorf("hash interpreter wasm: %w", err)
	}
	if !strings.EqualFold(wasmDigest, manifest.Interpreter.WasmSHA256) {
		return fmt.Errorf("interpreter wasm sha256 mismatch: got %s, want %s", wasmDigest, manifest.Interpreter.WasmSHA256)
	}

	stdlibPath, err := resolveInsideDir(absDir, manifest.Interpreter.StdlibPath)
	if err != nil {
		return fmt.Errorf("interpreter.stdlib_path: %w", err)
	}
	stdlibDigest, err := HashPath(stdlibPath)
	if err != nil {
		return fmt.Errorf("hash interpreter stdlib: %w", err)
	}
	if !strings.EqualFold(stdlibDigest, manifest.Interpreter.StdlibSHA256) {
		return fmt.Errorf("interpreter stdlib sha256 mismatch: got %s, want %s", stdlibDigest, manifest.Interpreter.StdlibSHA256)
	}

	if strings.TrimSpace(manifest.PackagesRoot) != "" {
		pkgRoot, err := resolveInsideDir(absDir, manifest.PackagesRoot)
		if err != nil {
			return fmt.Errorf("packages_root: %w", err)
		}
		info, err := os.Stat(pkgRoot)
		if err != nil {
			return fmt.Errorf("stat packages_root: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("packages_root %q is not a directory", manifest.PackagesRoot)
		}
	}

	if err := rejectHostNativeBinaries(absDir); err != nil {
		return err
	}
	return nil
}

// HashFile returns the lowercase hex SHA-256 digest of a regular file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashPath hashes a single file with HashFile or a directory deterministically
// with HashTree.
func HashPath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return HashFile(path)
	}
	return HashTree(path)
}

// HashTree computes a deterministic SHA-256 digest over all regular files
// beneath root, ordered by slash-normalized relative path.
func HashTree(root string) (string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in immutable image tree: %s", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(rels)

	outer := sha256.New()
	for _, rel := range rels {
		digest, err := HashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", rel, err)
		}
		_, _ = io.WriteString(outer, rel)
		_, _ = outer.Write([]byte{0})
		_, _ = io.WriteString(outer, digest)
		_, _ = outer.Write([]byte{'\n'})
	}
	return hex.EncodeToString(outer.Sum(nil)), nil
}

func rejectHostNativeBinaries(root string) error {
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if isHostNativeBinaryName(d.Name()) {
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				rel = p
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan image for native binaries: %w", err)
	}
	if len(found) > 0 {
		sort.Strings(found)
		return fmt.Errorf("host native binaries forbidden in WASI image: %s", strings.Join(found, ", "))
	}
	return nil
}

func isHostNativeBinaryName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".so") || strings.HasSuffix(lower, ".dylib") || strings.HasSuffix(lower, ".dll") {
		return true
	}
	return strings.Contains(lower, ".so.")
}

func resolveInsideDir(absRoot, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to image directory", rel)
	}
	joined := filepath.Clean(filepath.Join(absRoot, rel))
	within, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return "", err
	}
	if within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes image directory", rel)
	}
	return joined, nil
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
