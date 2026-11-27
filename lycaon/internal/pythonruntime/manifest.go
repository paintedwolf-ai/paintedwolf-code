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
	BundledFonts          []BundledFontSpec  `json:"bundled_fonts,omitempty"`
	NoticesPath           string             `json:"notices_path,omitempty"`
	StdlibCapabilities    []string           `json:"stdlib_capabilities,omitempty"`
	WasmImports           []WasmImportSpec   `json:"wasm_imports,omitempty"`
	ImportInventorySHA256 string             `json:"import_inventory_sha256,omitempty"`
}

// BundledFontSpec pins one redistributable font asset bundled inside the
// immutable image (for example DejaVuSans.ttf for fpdf2 Unicode PDF layout).
type BundledFontSpec struct {
	Family      string `json:"family"`
	Style       string `json:"style"`
	ImagePath   string `json:"image_path"`
	GuestPath   string `json:"guest_path"`
	SHA256      string `json:"sha256"`
	License     string `json:"license"`
	LicensePath string `json:"license_path,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
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

// VerifiedImage is an unforgeable, single-construction proof returned by
// VerifyImageResult for a specific (imageDir, manifest) pair. It is never
// cached globally or across Service constructions; callers outside
// pythonruntime cannot synthesize a verified instance.
type VerifiedImage struct {
	imageDir   string
	manifest   *ImageManifest
	wasmPath   string
	wasmSHA256 string
	wasmBytes  []byte
	snapshot   verifiedManifestSnapshot
	verified   bool
}

type verifiedManifestSnapshot struct {
	imageID               string
	wasmPath              string
	wasmSHA256            string
	meteredWasmSHA256     string
	stdlibPath            string
	stdlibSHA256          string
	packagesRoot          string
	packagesTreeSHA256    string
	importInventorySHA256 string
	hasBytecode           bool
	bytecodeMode          string
	bytecodeMagic         string
	bytecodeCount         int
	bytecodeTreeSHA256    string
}

func snapshotManifest(m *ImageManifest) verifiedManifestSnapshot {
	if m == nil {
		return verifiedManifestSnapshot{}
	}
	s := verifiedManifestSnapshot{
		imageID:               m.ImageID,
		wasmPath:              m.Interpreter.WasmPath,
		wasmSHA256:            strings.ToLower(strings.TrimSpace(m.Interpreter.WasmSHA256)),
		meteredWasmSHA256:     strings.ToLower(strings.TrimSpace(m.Interpreter.MeteredWasmSHA256)),
		stdlibPath:            m.Interpreter.StdlibPath,
		stdlibSHA256:          strings.ToLower(strings.TrimSpace(m.Interpreter.StdlibSHA256)),
		packagesRoot:          m.PackagesRoot,
		packagesTreeSHA256:    strings.ToLower(strings.TrimSpace(m.PackagesTreeSHA256)),
		importInventorySHA256: strings.ToLower(strings.TrimSpace(m.ImportInventorySHA256)),
	}
	if m.Bytecode != nil {
		s.hasBytecode = true
		s.bytecodeMode = m.Bytecode.InvalidationMode
		s.bytecodeMagic = strings.ToLower(strings.TrimSpace(m.Bytecode.MagicHex))
		s.bytecodeCount = m.Bytecode.PycCount
		s.bytecodeTreeSHA256 = strings.ToLower(strings.TrimSpace(m.Bytecode.PycTreeSHA256))
	}
	return s
}

// ImageDir returns the cleaned absolute image directory that was verified.
func (v *VerifiedImage) ImageDir() string {
	if v == nil {
		return ""
	}
	return v.imageDir
}

// Manifest returns the manifest that was verified.
func (v *VerifiedImage) Manifest() *ImageManifest {
	if v == nil {
		return nil
	}
	return v.manifest
}

// WasmSHA256 returns the verified lowercase hex SHA-256 digest of python.wasm.
func (v *VerifiedImage) WasmSHA256() string {
	if v == nil {
		return ""
	}
	return v.wasmSHA256
}

// WasmBytes returns the verified bytes of python.wasm read during
// VerifyImageResult.
func (v *VerifiedImage) WasmBytes() []byte {
	if v == nil {
		return nil
	}
	return v.wasmBytes
}

func (v *VerifiedImage) matches(imageDir string, manifest *ImageManifest) bool {
	if v == nil || !v.verified || manifest == nil {
		return false
	}
	absDir, err := filepath.Abs(imageDir)
	if err != nil || absDir != v.imageDir {
		return false
	}
	return v.snapshot == snapshotManifest(manifest)
}

// VerifyImage checks that the image directory matches manifest digests for
// python.wasm, stdlib, and declared package trees, and contains zero host
// native dynamic libraries (.so, .dylib, .dll).
func VerifyImage(dir string, manifest *ImageManifest) error {
	_, err := verifyImageInternal(dir, manifest, false)
	return err
}

// VerifyImageResult performs the full fail-closed image verification and
// returns a single-construction VerifiedImage carrying the verified python.wasm
// bytes and an unexported verification token for same-construction handoff.
func VerifyImageResult(dir string, manifest *ImageManifest) (*VerifiedImage, error) {
	return verifyImageInternal(dir, manifest, true)
}

func verifyImageInternal(dir string, manifest *ImageManifest, retainWasmBytes bool) (*VerifiedImage, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve image dir: %w", err)
	}

	wasmPath, err := resolveInsideDir(absDir, manifest.Interpreter.WasmPath)
	if err != nil {
		return nil, fmt.Errorf("interpreter.wasm_path: %w", err)
	}
	var wasmBytes []byte
	var wasmDigest string
	if retainWasmBytes {
		// #nosec G304 -- wasmPath is resolved inside absDir via resolveInsideDir.
		wasmBytes, err = os.ReadFile(wasmPath)
		if err != nil {
			return nil, fmt.Errorf("hash interpreter wasm: %w", err)
		}
		sum := sha256.Sum256(wasmBytes)
		wasmDigest = hex.EncodeToString(sum[:])
	} else {
		wasmDigest, err = HashFile(wasmPath)
		if err != nil {
			return nil, fmt.Errorf("hash interpreter wasm: %w", err)
		}
	}
	if !strings.EqualFold(wasmDigest, manifest.Interpreter.WasmSHA256) {
		return nil, fmt.Errorf("interpreter wasm sha256 mismatch: got %s, want %s", wasmDigest, manifest.Interpreter.WasmSHA256)
	}

	stdlibPath, err := resolveInsideDir(absDir, manifest.Interpreter.StdlibPath)
	if err != nil {
		return nil, fmt.Errorf("interpreter.stdlib_path: %w", err)
	}
	stdlibInfo, err := os.Stat(stdlibPath)
	if err != nil {
		return nil, fmt.Errorf("hash interpreter stdlib: %w", err)
	}
	stdlibIsDir := stdlibInfo.IsDir()

	var pkgRoot string
	if strings.TrimSpace(manifest.PackagesRoot) != "" {
		pkgRoot, err = resolveInsideDir(absDir, manifest.PackagesRoot)
		if err != nil {
			return nil, fmt.Errorf("packages_root: %w", err)
		}
		info, err := os.Stat(pkgRoot)
		if err != nil {
			return nil, fmt.Errorf("stat packages_root: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("packages_root %q is not a directory", manifest.PackagesRoot)
		}
		if !isHexSHA256(manifest.PackagesTreeSHA256) {
			return nil, fmt.Errorf("image manifest missing or invalid packages_tree_sha256 %q", manifest.PackagesTreeSHA256)
		}
	}

	var magicBytes []byte
	checkBytecode := manifest.Bytecode != nil
	if checkBytecode {
		magicBytes, err = hex.DecodeString(CPython312BytecodeMagicHex)
		if err != nil {
			return nil, err
		}
	}

	stdlibFiles := make(map[string]string)
	pkgFiles := make(map[string]string)
	pycFiles := make(map[string]string)
	var nativeBinaries []string
	var stdlibWalkErr error
	var pkgWalkErr error
	var pycWalkErr error
	buf := make([]byte, 32*1024)

	err = filepath.WalkDir(absDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		relAbs, err := filepath.Rel(absDir, p)
		if err != nil {
			return err
		}
		relAbsSlash := filepath.ToSlash(relAbs)
		if isHostNativeBinaryName(d.Name()) {
			nativeBinaries = append(nativeBinaries, relAbsSlash)
		}

		inStdlib, stdlibRel := relIfInsideDir(stdlibPath, p, stdlibIsDir)
		inPkg, pkgRel := relIfInsideDir(pkgRoot, p, pkgRoot != "")
		isPyc := checkBytecode && strings.HasSuffix(d.Name(), ".pyc")

		if !inStdlib && !inPkg && !isPyc {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if inStdlib && stdlibWalkErr == nil {
				stdlibWalkErr = fmt.Errorf("unexpected symlink in immutable image tree: %s", p)
			}
			if inPkg && pkgWalkErr == nil {
				pkgWalkErr = fmt.Errorf("unexpected symlink in immutable image tree: %s", p)
			}
			if isPyc && pycWalkErr == nil {
				pycWalkErr = fmt.Errorf("unexpected symlink .pyc in immutable image: %s", p)
			}
			return nil
		}

		digest, pycHdrErr, hashErr := hashFileWithBuf(p, relAbsSlash, buf, isPyc, magicBytes)
		if hashErr != nil {
			if inStdlib {
				return fmt.Errorf("hash interpreter stdlib: hash %s: %w", stdlibRel, hashErr)
			}
			if inPkg {
				return fmt.Errorf("hash packages_root tree: hash %s: %w", pkgRel, hashErr)
			}
			return fmt.Errorf("open bytecode %s: %w", relAbsSlash, hashErr)
		}
		if inStdlib {
			stdlibFiles[stdlibRel] = digest
		}
		if inPkg {
			pkgFiles[pkgRel] = digest
		}
		if isPyc {
			if pycHdrErr != nil && pycWalkErr == nil {
				pycWalkErr = pycHdrErr
			}
			pycFiles[relAbsSlash] = digest
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var stdlibDigest string
	if stdlibIsDir {
		if stdlibWalkErr != nil {
			return nil, fmt.Errorf("hash interpreter stdlib: %w", stdlibWalkErr)
		}
		stdlibDigest = hashDigestMap(stdlibFiles)
	} else {
		stdlibDigest, err = HashFile(stdlibPath)
		if err != nil {
			return nil, fmt.Errorf("hash interpreter stdlib: %w", err)
		}
	}
	if !strings.EqualFold(stdlibDigest, manifest.Interpreter.StdlibSHA256) {
		return nil, fmt.Errorf("interpreter stdlib sha256 mismatch: got %s, want %s", stdlibDigest, manifest.Interpreter.StdlibSHA256)
	}

	if pkgRoot != "" {
		if pkgWalkErr != nil {
			return nil, fmt.Errorf("hash packages_root tree: %w", pkgWalkErr)
		}
		pkgTreeDigest := hashDigestMap(pkgFiles)
		if !strings.EqualFold(pkgTreeDigest, manifest.PackagesTreeSHA256) {
			return nil, fmt.Errorf("packages_root tree sha256 mismatch: got %s, want %s", pkgTreeDigest, manifest.PackagesTreeSHA256)
		}
		for _, dist := range manifest.Distributions {
			if err := verifyDistributionTreeWithDigests(absDir, pkgRoot, dist, pkgFiles); err != nil {
				return nil, err
			}
		}
	}

	if manifest.Bytecode != nil {
		if err := verifyImageBytecodeWithDigests(manifest.Bytecode, pycFiles, pycWalkErr); err != nil {
			return nil, err
		}
	}

	if len(nativeBinaries) > 0 {
		sort.Strings(nativeBinaries)
		return nil, fmt.Errorf("host native binaries forbidden in WASI image: %s", strings.Join(nativeBinaries, ", "))
	}

	return &VerifiedImage{
		imageDir:   absDir,
		manifest:   manifest,
		wasmPath:   wasmPath,
		wasmSHA256: strings.ToLower(wasmDigest),
		wasmBytes:  wasmBytes,
		snapshot:   snapshotManifest(manifest),
		verified:   true,
	}, nil
}

func relIfInsideDir(root, p string, enabled bool) (bool, string) {
	if !enabled || root == "" {
		return false, ""
	}
	prefix := root + string(filepath.Separator)
	if !strings.HasPrefix(p, prefix) {
		return false, ""
	}
	return true, filepath.ToSlash(p[len(prefix):])
}

func hashFileWithBuf(path, relAbsSlash string, buf []byte, checkPyc bool, magicBytes []byte) (string, error, error) {
	// #nosec G304 -- path is walked inside absDir.
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	var hdr [16]byte
	totalRead := 0
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if checkPyc && totalRead < len(hdr) {
				copied := copy(hdr[totalRead:], buf[:n])
				_ = copied
			}
			totalRead += n
			_, _ = h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", nil, rerr
		}
	}
	var pycErr error
	if checkPyc {
		if totalRead < len(hdr) {
			eofErr := io.ErrUnexpectedEOF
			if totalRead == 0 {
				eofErr = io.EOF
			}
			pycErr = fmt.Errorf("truncated bytecode header in %s: %w", relAbsSlash, eofErr)
		} else if hdr[0] != magicBytes[0] || hdr[1] != magicBytes[1] || hdr[2] != magicBytes[2] || hdr[3] != magicBytes[3] {
			pycErr = fmt.Errorf("bytecode magic mismatch in %s: got %x want %s", relAbsSlash, hdr[:4], CPython312BytecodeMagicHex)
		} else {
			flags := uint32(hdr[4]) | uint32(hdr[5])<<8 | uint32(hdr[6])<<16 | uint32(hdr[7])<<24
			if (flags & 0x3) != 0x3 {
				pycErr = fmt.Errorf("bytecode %s is not PEP 552 checked-hash (flags=0x%x)", relAbsSlash, flags)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), pycErr, nil
}

func hashDigestMap(digests map[string]string) string {
	rels := make([]string, 0, len(digests))
	for rel := range digests {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	outer := sha256.New()
	for _, rel := range rels {
		_, _ = io.WriteString(outer, rel)
		_, _ = outer.Write([]byte{0})
		_, _ = io.WriteString(outer, digests[rel])
		_, _ = outer.Write([]byte{'\n'})
	}
	return hex.EncodeToString(outer.Sum(nil))
}

func verifyImageBytecodeWithDigests(spec *BytecodeSpec, pycFiles map[string]string, pycWalkErr error) error {
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
	if pycWalkErr != nil {
		return pycWalkErr
	}
	if len(pycFiles) != spec.PycCount {
		return fmt.Errorf("image bytecode count mismatch: got %d want %d", len(pycFiles), spec.PycCount)
	}
	gotTree := hashDigestMap(pycFiles)
	if !strings.EqualFold(gotTree, spec.PycTreeSHA256) {
		return fmt.Errorf("image bytecode pyc_tree_sha256 mismatch: got %s want %s", gotTree, spec.PycTreeSHA256)
	}
	return nil
}

func verifyDistributionTreeWithDigests(absDir, pkgRoot string, dist DistributionSpec, pkgFiles map[string]string) error {
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
	// #nosec G304 -- distInfoAbs is verified inside absDir via resolveInsideDir.
	recBytes, err := os.ReadFile(recPath)
	if err != nil {
		return fmt.Errorf("distribution %s missing RECORD: %w", dist.Name, err)
	}
	seen := make(map[string]struct{})
	var unique []string
	for _, line := range strings.Split(string(recBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rel := filepath.ToSlash(strings.SplitN(line, ",", 2)[0])
		if rel == "" {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		unique = append(unique, rel)
	}
	sort.Strings(unique)
	outer := sha256.New()
	for _, rel := range unique {
		full, err := resolveInsideDir(pkgRoot, filepath.FromSlash(rel))
		if err != nil {
			return fmt.Errorf("hash distribution %s tree: %w", dist.Name, err)
		}
		cleanRel, err := filepath.Rel(pkgRoot, full)
		if err != nil {
			return fmt.Errorf("hash distribution %s tree: %w", dist.Name, err)
		}
		digest, ok := pkgFiles[filepath.ToSlash(cleanRel)]
		if !ok {
			continue
		}
		_, _ = io.WriteString(outer, rel)
		_, _ = outer.Write([]byte{0})
		_, _ = io.WriteString(outer, digest)
		_, _ = outer.Write([]byte{'\n'})
	}
	digest := hex.EncodeToString(outer.Sum(nil))
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

func isHostNativeBinaryName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".so") || strings.HasSuffix(lower, ".dylib") || strings.HasSuffix(lower, ".dll") || strings.HasSuffix(lower, ".pyd") {
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
