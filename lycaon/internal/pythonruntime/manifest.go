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
// binary, stdlib, curated packages, digests, optional checked-hash bytecode,
// stdlib capability dispositions, and declared Wasm host imports.
type ImageManifest struct {
	SchemaVersion         string             `json:"schema_version"`
	ImageID               string             `json:"image_id"`
	BuiltAt               string             `json:"built_at"`
	Target                string             `json:"target"`
	Interpreter           InterpreterSpec    `json:"interpreter"`
	PackagesRoot          string             `json:"packages_root"`
	PackagesTreeSHA256    string             `json:"packages_tree_sha256,omitempty"`
	Bytecode              *BytecodeSpec      `json:"bytecode,omitempty"`
	Distributions         []DistributionSpec `json:"distributions"`
	StdlibCapabilities    []string           `json:"stdlib_capabilities,omitempty"`
	WasmImports           []WasmImportSpec   `json:"wasm_imports,omitempty"`
	ImportInventorySHA256 string             `json:"import_inventory_sha256,omitempty"`
}

// BytecodeSpec pins deterministic PEP 552 checked-hash CPython 3.12 .pyc
// bytecode bundled inside the immutable image tree.
type BytecodeSpec struct {
	InvalidationMode string `json:"invalidation_mode"`
	CPythonVersion   string `json:"cpython_version"`
	MagicHex         string `json:"magic_hex"`
	PycCount         int    `json:"pyc_count"`
	PycTreeSHA256    string `json:"pyc_tree_sha256"`
}

// CPython312BytecodeMagicHex is the little-endian 4-byte magic number (3531)
// of CPython 3.12.0 .pyc headers.
const CPython312BytecodeMagicHex = "cb0d0d0a"

// PatchSpec records a minimal, auditable patch applied to an image file.
type PatchSpec struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// InterpreterSpec records the CPython/WASI binary, stdlib archive/tree,
// provenance inputs, and applied patches.
type InterpreterSpec struct {
	Name              string            `json:"name"`
	Version           string            `json:"version"`
	WasmPath          string            `json:"wasm_path"`
	WasmSHA256        string            `json:"wasm_sha256"`
	MeteredWasmSHA256 string            `json:"metered_wasm_sha256,omitempty"`
	StdlibPath        string            `json:"stdlib_path"`
	StdlibSHA256      string            `json:"stdlib_sha256"`
	BuildInputs       map[string]string `json:"build_inputs,omitempty"`
	Patches           []PatchSpec       `json:"patches,omitempty"`
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
		if !isHexSHA256(manifest.PackagesTreeSHA256) {
			return fmt.Errorf("image manifest missing or invalid packages_tree_sha256 %q", manifest.PackagesTreeSHA256)
		}
		pkgTreeDigest, err := HashTree(pkgRoot)
		if err != nil {
			return fmt.Errorf("hash packages_root tree: %w", err)
		}
		if !strings.EqualFold(pkgTreeDigest, manifest.PackagesTreeSHA256) {
			return fmt.Errorf("packages_root tree sha256 mismatch: got %s, want %s", pkgTreeDigest, manifest.PackagesTreeSHA256)
		}
		for _, dist := range manifest.Distributions {
			if err := verifyDistributionTree(absDir, pkgRoot, dist); err != nil {
				return err
			}
		}
	}

	if manifest.Bytecode != nil {
		if err := verifyImageBytecode(absDir, manifest.Bytecode); err != nil {
			return err
		}
	}

	if err := rejectHostNativeBinaries(absDir); err != nil {
		return err
	}
	return nil
}

func verifyImageBytecode(absDir string, spec *BytecodeSpec) error {
	if spec.InvalidationMode != "checked-hash" {
		return fmt.Errorf("image bytecode invalidation_mode %q must be checked-hash", spec.InvalidationMode)
	}
	if !strings.EqualFold(spec.MagicHex, CPython312BytecodeMagicHex) {
		return fmt.Errorf("image bytecode magic_hex %q does not match CPython 3.12 (%s)", spec.MagicHex, CPython312BytecodeMagicHex)
	}
	if spec.PycCount <= 0 {
		return fmt.Errorf("image bytecode pyc_count must be positive (got %d)", spec.PycCount)
	}
	if !isHexSHA256(spec.PycTreeSHA256) {
		return fmt.Errorf("image bytecode invalid pyc_tree_sha256 %q", spec.PycTreeSHA256)
	}

	magicBytes, err := hex.DecodeString(CPython312BytecodeMagicHex)
	if err != nil {
		return err
	}
	var pycRels []string
	err = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".pyc") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink .pyc in immutable image: %s", p)
		}
		rel, err := filepath.Rel(absDir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		// Validate 16-byte PEP 552 checked-hash header: magic (4B) + flags (4B, bit0|bit1 set) + hash (8B).
		// #nosec G304 -- p is walked inside absDir.
		f, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("open bytecode %s: %w", relSlash, err)
		}
		var hdr [16]byte
		_, rerr := io.ReadFull(f, hdr[:])
		_ = f.Close()
		if rerr != nil {
			return fmt.Errorf("truncated bytecode header in %s: %w", relSlash, rerr)
		}
		if hdr[0] != magicBytes[0] || hdr[1] != magicBytes[1] || hdr[2] != magicBytes[2] || hdr[3] != magicBytes[3] {
			return fmt.Errorf("bytecode magic mismatch in %s: got %x want %s", relSlash, hdr[:4], CPython312BytecodeMagicHex)
		}
		flags := uint32(hdr[4]) | uint32(hdr[5])<<8 | uint32(hdr[6])<<16 | uint32(hdr[7])<<24
		if (flags & 0x3) != 0x3 {
			return fmt.Errorf("bytecode %s is not PEP 552 checked-hash (flags=0x%x)", relSlash, flags)
		}
		pycRels = append(pycRels, relSlash)
		return nil
	})
	if err != nil {
		return err
	}
	if len(pycRels) != spec.PycCount {
		return fmt.Errorf("image bytecode count mismatch: got %d want %d", len(pycRels), spec.PycCount)
	}
	gotTree, err := hashRelativeFiles(absDir, pycRels)
	if err != nil {
		return fmt.Errorf("hash image bytecode tree: %w", err)
	}
	if !strings.EqualFold(gotTree, spec.PycTreeSHA256) {
		return fmt.Errorf("image bytecode pyc_tree_sha256 mismatch: got %s want %s", gotTree, spec.PycTreeSHA256)
	}
	return nil
}

func verifyDistributionTree(absDir, pkgRoot string, dist DistributionSpec) error {
	if strings.TrimSpace(dist.DistInfoPath) == "" {
		return nil
	}
	distInfoAbs, err := resolveInsideDir(absDir, dist.DistInfoPath)
	if err != nil {
		return fmt.Errorf("distribution %s dist_info_path: %w", dist.Name, err)
	}
	info, err := os.Stat(distInfoAbs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("distribution %s dist_info_path %q missing or not a directory", dist.Name, dist.DistInfoPath)
	}
	if !isHexSHA256(dist.TreeSHA256) {
		return nil
	}
	recPath := filepath.Join(distInfoAbs, "RECORD")
	recBytes, err := os.ReadFile(recPath)
	if err != nil {
		return fmt.Errorf("distribution %s missing RECORD: %w", dist.Name, err)
	}
	var rels []string
	for _, line := range strings.Split(string(recBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rel := strings.SplitN(line, ",", 2)[0]
		if rel != "" {
			rels = append(rels, rel)
		}
	}
	digest, err := hashRelativeFiles(pkgRoot, rels)
	if err != nil {
		return fmt.Errorf("hash distribution %s tree: %w", dist.Name, err)
	}
	if !strings.EqualFold(digest, dist.TreeSHA256) {
		return fmt.Errorf("distribution %s tree sha256 mismatch: got %s, want %s", dist.Name, digest, dist.TreeSHA256)
	}
	return nil
}

func hashRelativeFiles(root string, rels []string) (string, error) {
	seen := make(map[string]struct{}, len(rels))
	unique := make([]string, 0, len(rels))
	for _, r := range rels {
		r = filepath.ToSlash(r)
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		unique = append(unique, r)
	}
	sort.Strings(unique)
	outer := sha256.New()
	for _, rel := range unique {
		full, err := resolveInsideDir(root, filepath.FromSlash(rel))
		if err != nil {
			return "", err
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		digest, err := HashFile(full)
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
