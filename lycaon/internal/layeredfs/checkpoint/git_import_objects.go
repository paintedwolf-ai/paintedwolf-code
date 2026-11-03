package checkpoint

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"golang.org/x/sys/unix"
)

const (
	maxVisitedGitObjects, maxMetadataObjectBytes, maxLooseBlobBytes = 65536, 16 << 20, 512 << 20
)

// ApplyPathXattrs applies extended attributes to abs, ignoring ENOTSUP/EOPNOTSUPP.
func ApplyPathXattrs(abs string, xattrs map[string][]byte) error {
	for k, v := range xattrs {
		if err := unix.Setxattr(abs, k, v, 0); err != nil && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) {
			return err
		}
	}
	return nil
}

// WriteLooseGitObject writes a standard zlib-compressed loose Git object into objectsDir and returns its SHA-1 hex OID.
func WriteLooseGitObject(objectsDir, objType string, payload []byte) (string, error) {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(payload))), payload...)
	oid := hexObjSHA1(raw)
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, wErr := zw.Write(raw)
	if err := errors.Join(wErr, zw.Close(), os.MkdirAll(filepath.Join(objectsDir, oid[:2]), 0o755)); err != nil {
		return "", err
	}
	return oid, writeAtomicJSONOrText(filepath.Join(objectsDir, oid[:2]), oid[2:], compressed.Bytes(), 0o444)
}

func ensureMinimalGitDirLayout(gitDir, commonGitDir string) error {
	if err := errors.Join(os.MkdirAll(filepath.Join(commonGitDir, "objects"), 0o755), os.MkdirAll(filepath.Join(commonGitDir, "refs"), 0o755)); err != nil {
		return err
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

func copyCandidateGitObjects(storeDir, checkpointID, targetObjectsDir string) error {
	if storeDir = strings.TrimSpace(storeDir); storeDir == "" {
		return nil
	}
	candidates := []string{filepath.Join(storeDir, "objects")}
	if checkpointID != "" {
		candidates = append([]string{filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects")}, candidates...)
	}
	for _, srcObjects := range candidates {
		st, err := os.Lstat(srcObjects)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || filepath.Clean(srcObjects) == filepath.Clean(targetObjectsDir) {
			continue
		}
		walkErr := filepath.WalkDir(srcObjects, func(path string, d os.DirEntry, wErr error) error {
			if wErr != nil || d.IsDir() {
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
			if _, statErr := os.Stat(dst); statErr == nil {
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
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
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
	_, copyErr := io.Copy(tmpFile, in)
	if err := errors.Join(copyErr, tmpFile.Chmod(mode), syncFile(tmpFile), tmpFile.Close(), os.Rename(tmpPath, dstPath)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(filepath.Dir(dstPath))
}

func verifyReachableGitObject(ctx context.Context, repoRoot, commonGitDir, objectsDir, oid string, seen map[string]bool) error {
	oid = strings.ToLower(strings.TrimSpace(oid))
	if !isHexOID(oid) || isAllZeroOID(oid) {
		return fmt.Errorf("checkpoint: invalid git object id %q", oid)
	}
	if seen[oid] {
		return nil
	}
	if len(seen) >= maxVisitedGitObjects {
		return fmt.Errorf("checkpoint: git object traversal exceeded %d objects", maxVisitedGitObjects)
	}
	var (
		objType string
		body    []byte
		err     error
	)
	if f, openErr := os.Open(filepath.Join(objectsDir, oid[:2], oid[2:])); openErr == nil {
		objType, body, err = readAndVerifyLooseGitObject(f, oid)
		_ = f.Close()
	} else if errors.Is(openErr, os.ErrNotExist) {
		objType, body, err = readAndVerifyPackedGitObject(ctx, repoRoot, commonGitDir, oid)
	} else {
		err = openErr
	}
	if err != nil {
		return err
	}
	seen[oid] = true
	switch objType {
	case "blob":
		return nil
	case "commit", "tag":
		return verifyCommitOrTagBody(ctx, repoRoot, commonGitDir, objectsDir, body, objType == "commit", seen)
	case "tree":
		return verifyTreeBody(ctx, repoRoot, commonGitDir, objectsDir, body, len(oid)/2, seen)
	default:
		return fmt.Errorf("checkpoint: unsupported git object type %q", objType)
	}
}

func readAndVerifyLooseGitObject(f *os.File, oid string) (string, []byte, error) {
	zr, err := zlib.NewReader(f)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = zr.Close() }()
	h := sha1.New()
	if len(oid) == 64 {
		h = sha256.New()
	}
	tr := io.TeeReader(zr, h)
	var hdrBuf [64]byte
	hdrLen, foundNul := 0, false
	for hdrLen < len(hdrBuf) {
		var b [1]byte
		if _, rErr := io.ReadFull(tr, b[:]); rErr != nil {
			return "", nil, fmt.Errorf("malformed loose object header for %s: %w", oid, rErr)
		}
		if b[0] == 0 {
			foundNul = true
			break
		}
		hdrBuf[hdrLen] = b[0]
		hdrLen++
	}
	hdrType, hdrSize, ok := strings.Cut(string(hdrBuf[:hdrLen]), " ")
	size, sErr := strconv.ParseInt(hdrSize, 10, 64)
	if !foundNul || !ok || sErr != nil || size < 0 {
		return "", nil, fmt.Errorf("malformed loose object header for %s", oid)
	}
	var body []byte
	if hdrType == "blob" {
		if n, cErr := io.Copy(io.Discard, io.LimitReader(tr, size+1)); size > maxLooseBlobBytes || cErr != nil || n != size {
			return "", nil, fmt.Errorf("size or limit mismatch for loose blob %s", oid)
		}
	} else {
		var rErr error
		if body, rErr = io.ReadAll(io.LimitReader(tr, size+1)); size > maxMetadataObjectBytes || rErr != nil || int64(len(body)) != size {
			return "", nil, fmt.Errorf("size or limit mismatch for loose object %s", oid)
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != oid {
		return "", nil, fmt.Errorf("digest mismatch for loose object %s", oid)
	}
	return hdrType, body, nil
}

func readAndVerifyPackedGitObject(ctx context.Context, repoRoot, commonGitDir, oid string) (string, []byte, error) {
	typeOut, code, err := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", "-t", oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		return "", nil, fmt.Errorf("missing git object %s: %w (%s)", oid, err, strings.TrimSpace(string(typeOut)))
	}
	objType := strings.TrimSpace(string(typeOut))
	if objType == "blob" {
		diag, bCode, bErr := gitexec.RunTo(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", "blob", oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic}, io.Discard)
		if bErr != nil || bCode != 0 {
			return "", nil, fmt.Errorf("corrupt packed blob %s: %w (%s)", oid, bErr, strings.TrimSpace(string(diag)))
		}
		return "blob", nil, nil
	}
	body, cCode, cErr := gitexec.Run(ctx, repoRoot, []string{"--git-dir=" + commonGitDir, "cat-file", objType, oid}, gitexec.Opts{Profile: gitexec.ProfileHermetic, MaxOutput: maxMetadataObjectBytes})
	if cErr != nil || cCode != 0 {
		return "", nil, fmt.Errorf("corrupt packed %s %s: %w", objType, oid, cErr)
	}
	return objType, body, nil
}

func verifyCommitOrTagBody(ctx context.Context, repoRoot, commonGitDir, objectsDir string, body []byte, isCommit bool, seen map[string]bool) error {
	foundPrimary := false
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			break
		}
		f := strings.Fields(line)
		if len(f) == 0 || !((isCommit && (f[0] == "tree" || f[0] == "parent")) || (!isCommit && f[0] == "object")) {
			continue
		}
		if len(f) != 2 || !isHexOID(strings.ToLower(f[1])) || isAllZeroOID(strings.ToLower(f[1])) {
			return fmt.Errorf("checkpoint: invalid %s target oid %q", f[0], line)
		}
		foundPrimary = foundPrimary || f[0] == "tree" || f[0] == "object"
		if err := verifyReachableGitObject(ctx, repoRoot, commonGitDir, objectsDir, strings.ToLower(f[1]), seen); err != nil {
			return err
		}
	}
	if !foundPrimary {
		return fmt.Errorf("checkpoint: missing required target object header")
	}
	return nil
}

func verifyTreeBody(ctx context.Context, repoRoot, commonGitDir, objectsDir string, body []byte, hashBytes int, seen map[string]bool) error {
	for len(body) > 0 {
		n := bytes.IndexByte(body, 0)
		if n <= 0 || n+1+hashBytes > len(body) {
			return fmt.Errorf("checkpoint: malformed tree entry")
		}
		hdr := string(body[:n])
		mode, name, ok := strings.Cut(hdr, " ")
		if !ok || mode == "" || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return fmt.Errorf("checkpoint: invalid tree entry header %q", hdr)
		}
		entryOID := hex.EncodeToString(body[n+1 : n+1+hashBytes])
		body = body[n+1+hashBytes:]
		if !isHexOID(entryOID) || isAllZeroOID(entryOID) {
			return fmt.Errorf("checkpoint: invalid tree entry oid %q", entryOID)
		}
		if mode != "160000" {
			if err := verifyReachableGitObject(ctx, repoRoot, commonGitDir, objectsDir, entryOID, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func hexObjSHA1(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
func isHexOID(s string) bool     { _, err := hex.DecodeString(s); return err == nil && (len(s) == 40 || len(s) == 64) }
func isAllZeroOID(s string) bool { return strings.Trim(s, "0") == "" }

func selectPrimaryRootDir(targetRoots map[string]string) string {
	for _, k := range []string{"primary", "root-0", ""} {
		if d := strings.TrimSpace(targetRoots[k]); d != "" {
			return d
		}
	}
	for _, d := range targetRoots {
		if d = strings.TrimSpace(d); d != "" {
			return d
		}
	}
	return ""
}
