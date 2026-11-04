package checkpoint

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" // #nosec G505 -- SHA-1 is the object identifier in the admitted Git repository format.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const (
	maxVisitedGitObjects, maxMetadataObjectBytes, maxLooseBlobBytes = 65536, 16 << 20, 512 << 20
)

var (
	wrapObjectCopyReaderFn = func(_ string, r io.Reader) io.Reader { return r }
	syncObjectFileFn       = syncFile
	closeObjectFileFn      = func(f *os.File) error { return f.Close() }
)

// WriteLooseGitObject writes a standard zlib-compressed loose Git object into objectsDir and returns its SHA-1 hex OID.
func WriteLooseGitObject(objectsDir, objType string, payload []byte) (string, error) {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(payload))), payload...)
	oid := hexObjSHA1(raw)
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, wErr := zw.Write(raw)
	if err := errors.Join(wErr, zw.Close()); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(objectsDir, oid[:2]), 0o750); err != nil {
		return "", err
	}
	return oid, writeAtomicJSONOrText(filepath.Join(objectsDir, oid[:2]), oid[2:], compressed.Bytes(), 0o444)
}

func ensureMinimalGitDirLayout(gitDir, commonGitDir string) error {
	for _, d := range []string{filepath.Join(commonGitDir, "objects"), filepath.Join(commonGitDir, "refs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Join(commonGitDir, "config")); errors.Is(err, os.ErrNotExist) {
		if wErr := writeAtomicJSONOrText(commonGitDir, "config", []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644); wErr != nil {
			return wErr
		}
	}
	for _, d := range []string{commonGitDir, gitDir} {
		if _, err := os.Lstat(filepath.Join(d, "HEAD")); errors.Is(err, os.ErrNotExist) {
			if wErr := writeAtomicJSONOrText(d, "HEAD", []byte("ref: refs/heads/main\n"), 0o644); wErr != nil {
				return wErr
			}
		}
	}
	return nil
}

func copyCandidateGitObjects(storeDir, checkpointID, rootID string, objectCheckpoints []string, targetObjectsDir string) error {
	if storeDir = strings.TrimSpace(storeDir); storeDir == "" {
		return nil
	}
	candidates := []string{filepath.Join(storeDir, "objects")}
	for _, id := range append([]string{checkpointID}, objectCheckpoints...) {
		if id != "" {
			candidates = append([]string{CheckpointGitObjectsDir(storeDir, id, rootID)}, candidates...)
		}
	}
	for _, srcObjects := range candidates {
		st, err := os.Lstat(srcObjects)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || filepath.Clean(srcObjects) == filepath.Clean(targetObjectsDir) {
			continue
		}
		walkErr := filepath.WalkDir(srcObjects, func(path string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				return wErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("checkpoint: symlink prohibited in retained git objects: %s", path)
			}
			rel, rErr := filepath.Rel(srcObjects, path)
			if rErr != nil || filepath.ToSlash(rel) == "info/alternates" {
				return rErr
			}
			dst := filepath.Join(targetObjectsDir, rel)
			if isValidDestinationGitAsset(path, dst) {
				return nil
			}
			return copyFileStreamingAtomic(path, dst, 0o444)
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

func copyFileStreamingAtomic(srcPath, dstPath string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmpFile, err := os.CreateTemp(filepath.Dir(dstPath), "."+filepath.Base(dstPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	published := false
	defer func() {
		_ = tmpFile.Close()
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmpFile, wrapObjectCopyReaderFn(srcPath, in)); err != nil {
		return err
	}
	if err := tmpFile.Chmod(mode); err != nil {
		return err
	}
	if err := errors.Join(syncObjectFileFn(tmpFile), closeObjectFileFn(tmpFile)); err != nil {
		return err
	}
	if err := publishStagedGitAsset(srcPath, tmpPath, dstPath); err != nil {
		return err
	}
	published = true
	return syncDir(filepath.Dir(dstPath))
}

func publishStagedGitAsset(srcPath, tmpPath, dstPath string) error {
	if oid, ok := looseObjectOIDForPath(dstPath); ok {
		f, err := os.Open(tmpPath)
		if err != nil {
			return err
		}
		_, _, vErr := readAndVerifyLooseGitObject(f, oid)
		_ = f.Close()
		if vErr != nil {
			return vErr
		}
	}
	if linkErr := os.Link(tmpPath, dstPath); linkErr == nil {
		_ = os.Remove(tmpPath)
		return nil
	} else if errors.Is(linkErr, os.ErrExist) && isValidDestinationGitAsset(srcPath, dstPath) {
		_ = os.Remove(tmpPath)
		return nil
	}
	return fseffect.Rename(filepath.Dir(dstPath), filepath.Base(tmpPath), filepath.Base(dstPath))
}

func isValidDestinationGitAsset(srcPath, dstPath string) bool {
	dstSt, err := os.Lstat(dstPath)
	if err != nil || !dstSt.Mode().IsRegular() || dstSt.Size() <= 0 {
		return false
	}
	if oid, ok := looseObjectOIDForPath(dstPath); ok {
		f, openErr := os.Open(dstPath)
		if openErr != nil {
			return false
		}
		_, _, vErr := readAndVerifyLooseGitObject(f, oid)
		_ = f.Close()
		return vErr == nil
	}
	srcSt, err := os.Lstat(srcPath)
	if err != nil || !srcSt.Mode().IsRegular() || srcSt.Size() != dstSt.Size() {
		return false
	}
	srcSHA, sErr := fileDigestSHA256(srcPath)
	dstSHA, dErr := fileDigestSHA256(dstPath)
	return sErr == nil && dErr == nil && srcSHA != "" && srcSHA == dstSHA
}

func looseObjectOIDForPath(p string) (string, bool) {
	prefix, suffix := filepath.Base(filepath.Dir(p)), filepath.Base(p)
	oid := strings.ToLower(prefix + suffix)
	return oid, len(prefix) == 2 && isHexOID(oid) && !isAllZeroOID(oid)
}

func fileDigestSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, cErr := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), cErr
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("checkpoint: read %s failed or exceeded limit: %w", path, err)
	}
	return data, nil
}

func hexObjSHA1(b []byte) string {
	s := sha1.Sum(b) /* #nosec G401 -- Standard Git object identifier. */
	return hex.EncodeToString(s[:])
}
func isHexOID(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && (len(s) == 40 || len(s) == 64)
}
func isAllZeroOID(s string) bool { return strings.Trim(s, "0") == "" }

// CheckpointGitObjectsDir is where a checkpoint keeps the objects its refs add for one root; an empty
// root ID is the primary root.
func CheckpointGitObjectsDir(storeDir, checkpointID, rootID string) string {
	if rootID == "" {
		return filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects")
	}
	return filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects-roots", rootID)
}

func selectPrimaryRootDir(targetRoots map[string]string) string {
	return strings.TrimSpace(targetRoots[selectPrimaryRootID(targetRoots)])
}

func selectPrimaryRootID(targetRoots map[string]string) string {
	for _, k := range []string{"primary", "root-0", ""} {
		if strings.TrimSpace(targetRoots[k]) != "" {
			return k
		}
	}
	for _, id := range slices.Sorted(maps.Keys(targetRoots)) {
		if strings.TrimSpace(targetRoots[id]) != "" {
			return id
		}
	}
	return ""
}
