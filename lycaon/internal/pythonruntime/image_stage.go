package pythonruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxImageArchiveBytes = 512 << 20

// ResolveRetainedDevArtifact searches explicit local artifact directories,
// PW_PORTABLE_PYTHON_ARTIFACT_DIR, and the canonical retained development
// producer directory (~/.local/share/paintedwolf-portable-python/results/{PY07,PY08/image})
// for a pinned archive file by exact filename. Returns empty string if not found.
func ResolveRetainedDevArtifact(filename string, extraDirs ...string) string {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return ""
	}
	var searchDirs []string
	for _, d := range extraDirs {
		if trimmed := strings.TrimSpace(d); trimmed != "" {
			searchDirs = append(searchDirs,
				trimmed,
				filepath.Join(trimmed, "PY07"),
				filepath.Join(trimmed, "PY08", "image"),
			)
		}
	}
	if envDir := strings.TrimSpace(os.Getenv(EnvArtifactDir)); envDir != "" {
		searchDirs = append(searchDirs,
			envDir,
			filepath.Join(envDir, "PY07"),
			filepath.Join(envDir, "PY08", "image"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		base := filepath.Join(home, ".local", "share", "paintedwolf-portable-python", "results")
		searchDirs = append(searchDirs,
			filepath.Join(base, "PY07"),
			filepath.Join(base, "PY08", "image"),
		)
	}
	for _, dir := range searchDirs {
		cand := filepath.Join(dir, filename)
		if info, err := os.Stat(cand); err == nil && info.Mode().IsRegular() {
			return cand
		}
	}
	return ""
}

// StageImageArtifact stages and verifies the pinned checked-hash CPython 3.12
// image into <stageRoot>/portable-python/image, cleanly replacing any prior or
// stale files. It fails closed if the image is missing, unpinned, stale, or
// fails checked-hash bytecode or digest verification.
//
// Resolution order:
//  1. Explicit imageSource (a .tar.gz archive or pre-extracted image directory)
//  2. Retained development producer artifact (PW_PORTABLE_PYTHON_ARTIFACT_DIR or
//     ~/.local/share/paintedwolf-portable-python/results/PY08/image/<archive_name>)
//  3. Pinned HTTPS release URL in m.Image.URL (production release publication)
func StageImageArtifact(
	ctx context.Context,
	m *BundledEngineManifest,
	imageSource string,
	stageRoot string,
	artifactDir string,
	client *http.Client,
) (string, error) {
	if err := ValidateBundledEngineManifest(m); err != nil {
		return "", fmt.Errorf("stage image: invalid bundled manifest: %w", err)
	}
	if m.Image == nil {
		return "", errors.New("stage image: bundled manifest does not pin portable_python_image")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(stageRoot) == "" {
		return "", errors.New("stage image: stage root is required")
	}

	parentDir := filepath.Join(stageRoot, "portable-python")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", fmt.Errorf("create portable-python stage dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp(parentDir, ".image-staging-*")
	if err != nil {
		return "", fmt.Errorf("create image staging temp dir: %w", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	src := strings.TrimSpace(imageSource)
	if src == "" {
		if devPath := ResolveRetainedDevArtifact(m.Image.ArchiveName, artifactDir); devPath != "" {
			src = devPath
		} else if strings.TrimSpace(m.Image.URL) != "" {
			src = strings.TrimSpace(m.Image.URL)
		}
	}
	if src == "" {
		return "", fmt.Errorf("stage image: checked-hash image %q not found in retained dev artifacts (%s / ~/.local/share/paintedwolf-portable-python/results/PY08/image) and no release URL configured", m.Image.ArchiveName, EnvArtifactDir)
	}

	if info, statErr := os.Stat(src); statErr == nil && info.IsDir() {
		if err := copyImageDir(src, tmpDir); err != nil {
			return "", fmt.Errorf("copy image dir %s: %w", src, err)
		}
	} else {
		raw, err := readSourceBytes(ctx, src, client)
		if err != nil {
			return "", fmt.Errorf("obtain checked-hash image %q from %s (development builds expect retained producer artifact in ~/.local/share/paintedwolf-portable-python/results/PY08/image/ or %s; production builds require published release URL): %w", m.Image.ArchiveName, src, EnvArtifactDir, err)
		}
		if m.Image.ArchiveBytes > 0 && int64(len(raw)) != m.Image.ArchiveBytes {
			return "", fmt.Errorf("checked-hash image archive %s size mismatch: got %d want %d", m.Image.ArchiveName, len(raw), m.Image.ArchiveBytes)
		}
		gotSHA := sha256Hex(raw)
		if !strings.EqualFold(gotSHA, m.Image.ArchiveSHA256) {
			return "", fmt.Errorf("checked-hash image archive %s sha256 mismatch: got %s want %s", m.Image.ArchiveName, gotSHA, m.Image.ArchiveSHA256)
		}
		if err := extractImageTarGz(raw, tmpDir); err != nil {
			return "", fmt.Errorf("extract checked-hash image archive: %w", err)
		}
	}

	// Verify the freshly staged tree completely BEFORE replacing the live target.
	if err := VerifyStagedImageDir(m, tmpDir); err != nil {
		return "", fmt.Errorf("verify staged checked-hash image before commit: %w", err)
	}

	destDir := StagedImageDir(stageRoot)
	if err := replaceDirectoryAtomic(tmpDir, destDir); err != nil {
		return "", fmt.Errorf("commit staged image directory %s: %w", destDir, err)
	}
	cleanupTmp = false

	return VerifyStagedImageArtifact(m, stageRoot)
}

// VerifyStagedImageArtifact verifies <stageRoot>/portable-python/image against
// the pinned checked-hash .pyc BundledImageSpec and full VerifyImage checks.
func VerifyStagedImageArtifact(m *BundledEngineManifest, stageRoot string) (string, error) {
	dir := StagedImageDir(stageRoot)
	if err := VerifyStagedImageDir(m, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// VerifyStagedImageDir validates an extracted image directory against m.Image
// and VerifyImage (failing closed on missing, stale, tampered, or non-pyc images).
func VerifyStagedImageDir(m *BundledEngineManifest, imageDir string) error {
	if err := ValidateBundledEngineManifest(m); err != nil {
		return err
	}
	if m.Image == nil {
		return errors.New("bundled manifest does not pin portable_python_image")
	}
	manifestPath := filepath.Join(imageDir, "image-manifest.json")
	gotManSHA, err := HashFile(manifestPath)
	if err != nil {
		return fmt.Errorf("hash staged image-manifest.json: %w", err)
	}
	if !strings.EqualFold(gotManSHA, m.Image.ManifestSHA256) {
		return fmt.Errorf("staged image-manifest.json sha256 mismatch (stale or wrong image): got %s want %s", gotManSHA, m.Image.ManifestSHA256)
	}
	im, err := LoadImageManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load staged image-manifest.json: %w", err)
	}
	if im.ImageID != m.Image.ImageID {
		return fmt.Errorf("staged image_id mismatch (stale image): got %q want %q", im.ImageID, m.Image.ImageID)
	}
	if !strings.EqualFold(im.Interpreter.WasmSHA256, m.Image.CPythonWasmSHA256) {
		return fmt.Errorf("staged image cpython wasm sha256 mismatch: got %s want %s", im.Interpreter.WasmSHA256, m.Image.CPythonWasmSHA256)
	}
	if !strings.EqualFold(im.Interpreter.MeteredWasmSHA256, m.Image.MeteredWasmSHA256) {
		return fmt.Errorf("staged image metered wasm sha256 mismatch: got %s want %s", im.Interpreter.MeteredWasmSHA256, m.Image.MeteredWasmSHA256)
	}
	if !strings.EqualFold(im.ImportInventorySHA256, m.Image.ImportInventorySHA256) {
		return fmt.Errorf("staged image import_inventory_sha256 mismatch: got %s want %s", im.ImportInventorySHA256, m.Image.ImportInventorySHA256)
	}
	if im.Bytecode == nil {
		return errors.New("staged image lacks required checked-hash CPython 3.12 bytecode metadata")
	}
	if im.Bytecode.InvalidationMode != m.Image.BytecodeInvalidationMode ||
		!strings.EqualFold(im.Bytecode.MagicHex, m.Image.BytecodeMagicHex) ||
		im.Bytecode.PycCount != m.Image.PycCount ||
		!strings.EqualFold(im.Bytecode.PycTreeSHA256, m.Image.PycTreeSHA256) {
		return fmt.Errorf("staged image bytecode spec mismatch: got mode=%q magic=%s count=%d tree=%s want mode=%q magic=%s count=%d tree=%s",
			im.Bytecode.InvalidationMode, im.Bytecode.MagicHex, im.Bytecode.PycCount, im.Bytecode.PycTreeSHA256,
			m.Image.BytecodeInvalidationMode, m.Image.BytecodeMagicHex, m.Image.PycCount, m.Image.PycTreeSHA256)
	}
	if err := VerifyImage(imageDir, im); err != nil {
		return fmt.Errorf("verify staged image tree and checked-hash bytecode: %w", err)
	}
	return nil
}

func extractImageTarGz(archive []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var totalBytes int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		name = strings.TrimPrefix(name, "image/")
		if name == "" || name == "." || name == "image" {
			continue
		}
		targetPath, err := resolveInsideDir(destDir, filepath.FromSlash(name))
		if err != nil {
			return fmt.Errorf("unsafe tar path %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			totalBytes += hdr.Size
			if totalBytes > maxImageArchiveBytes {
				return fmt.Errorf("extracted image exceeds size limit (%d bytes)", maxImageArchiveBytes)
			}
			mode := os.FileMode(hdr.Mode & 0o755)
			if mode == 0 {
				mode = 0o644
			}
			// #nosec G304 -- targetPath verified inside destDir by resolveInsideDir.
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, hdr.Size+1)); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type %d for %q in immutable image", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

func copyImageDir(srcDir, destDir string) error {
	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in image source: %s", p)
		}
		dst := filepath.Join(destDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		// #nosec G304 -- p walked inside srcDir.
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

// replaceDirectoryAtomic swaps tmpDir into destDir, removing any pre-existing
// directory (and any stale files inside it).
func replaceDirectoryAtomic(tmpDir, destDir string) error {
	backup := ""
	if _, err := os.Lstat(destDir); err == nil {
		backup = destDir + ".old-staging"
		_ = os.RemoveAll(backup)
		if err := os.Rename(destDir, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpDir, destDir); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destDir)
		}
		return err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}
