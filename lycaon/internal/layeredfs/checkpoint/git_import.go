package checkpoint

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
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

// ResolveGitDirs resolves the worktree gitDir and shared commonGitDir for repoRoot (supporting linked-worktree .git files).
func ResolveGitDirs(repoRoot string) (gitDir, commonGitDir string, err error) {
	dotGit := filepath.Join(repoRoot, ".git")
	st, err := os.Lstat(dotGit)
	if err != nil {
		return "", "", fmt.Errorf("checkpoint: resolve .git at %s: %w", dotGit, err)
	}
	if st.IsDir() {
		gitDir = dotGit
	} else if st.Mode().IsRegular() {
		raw, rErr := os.ReadFile(dotGit)
		line := strings.TrimSpace(string(raw))
		if rErr != nil || !strings.HasPrefix(line, "gitdir:") {
			return "", "", fmt.Errorf("checkpoint: invalid .git file at %s", dotGit)
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(repoRoot, target)
		}
		gitDir = filepath.Clean(target)
	} else {
		return "", "", fmt.Errorf("checkpoint: unsupported .git entry at %s", dotGit)
	}
	if gst, sErr := os.Stat(gitDir); sErr != nil || !gst.IsDir() {
		return "", "", fmt.Errorf("checkpoint: gitdir %s unavailable", gitDir)
	}
	commonGitDir = gitDir
	if rawCommon, cErr := os.ReadFile(filepath.Join(gitDir, "commondir")); cErr == nil {
		if rel := strings.TrimSpace(string(rawCommon)); rel != "" {
			if !filepath.IsAbs(rel) {
				rel = filepath.Join(gitDir, rel)
			}
			commonGitDir = filepath.Clean(rel)
		}
	}
	if cst, sErr := os.Stat(commonGitDir); sErr != nil || !cst.IsDir() {
		return "", "", fmt.Errorf("checkpoint: commondir %s unavailable", commonGitDir)
	}
	return gitDir, commonGitDir, nil
}

// ImportAndValidateCheckpointGitRefs imports missing Git objects, validates object closure, and writes checkpoint/worker refs.
func ImportAndValidateCheckpointGitRefs(storeDir, checkpointID, workerID string, git PrivateGitSnapshot, targetRoots map[string]string) (map[string]string, error) {
	if len(git.Refs) == 0 {
		return nil, nil
	}
	cleanWorker, err := cleanNamespaceSegment(workerID, "worker")
	if err != nil {
		return nil, err
	}
	cleanCheckpoint := ""
	if strings.TrimSpace(checkpointID) != "" {
		if cleanCheckpoint, err = cleanNamespaceSegment(checkpointID, "checkpoint"); err != nil {
			return nil, err
		}
	}
	primaryDir := selectPrimaryRootDir(targetRoots)
	gitDir, commonGitDir, err := ResolveGitDirs(primaryDir)
	if err != nil {
		return nil, err
	}
	targetObjectsDir := filepath.Join(commonGitDir, "objects")
	if err := errors.Join(os.MkdirAll(targetObjectsDir, 0o755), copyCandidateGitObjects(storeDir, targetObjectsDir)); err != nil {
		return nil, err
	}
	refNames := make([]string, 0, len(git.Refs))
	for name := range git.Refs {
		refNames = append(refNames, name)
	}
	sort.Strings(refNames)

	verifiedOIDs := make(map[string]bool, len(refNames)*2)
	type plannedRefWrite struct{ dstRef, oid string }
	var writes []plannedRefWrite
	for _, refName := range refNames {
		oid := strings.ToLower(strings.TrimSpace(git.Refs[refName]))
		if !isHexOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid git oid %q for ref %q", oid, refName)
		}
		if err := verifyReachableGitObject(targetObjectsDir, oid, verifiedOIDs); err != nil {
			return nil, fmt.Errorf("checkpoint: unreachable or corrupt git object for ref %s (%s): %w", refName, oid, err)
		}
		cleanRel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(refName)), "/"), "refs/")))
		if cleanRel == "" || cleanRel == "." || strings.HasPrefix(cleanRel, "..") {
			return nil, fmt.Errorf("checkpoint: invalid git ref name %q", refName)
		}
		writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/workers/" + cleanWorker + "/" + cleanRel, oid: oid})
		if cleanCheckpoint != "" {
			writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/checkpoints/" + cleanCheckpoint + "/" + cleanRel, oid: oid})
		}
	}
	for _, w := range writes {
		for _, baseDir := range []string{commonGitDir, gitDir} {
			if raw, rErr := os.ReadFile(filepath.Join(baseDir, filepath.FromSlash(w.dstRef))); rErr == nil {
				if existing := strings.ToLower(strings.TrimSpace(string(raw))); existing != w.oid {
					return nil, fmt.Errorf("%w: %s has %s, want %s", ErrCheckpointRefConflict, w.dstRef, existing, w.oid)
				}
			}
		}
	}
	imported := make(map[string]string, len(writes))
	for _, w := range writes {
		refPath := filepath.Join(commonGitDir, filepath.FromSlash(w.dstRef))
		if raw, rErr := os.ReadFile(refPath); rErr != nil || strings.ToLower(strings.TrimSpace(string(raw))) != w.oid {
			if err := writeAtomicJSONOrText(filepath.Dir(refPath), filepath.Base(refPath), []byte(w.oid+"\n"), 0o644); err != nil {
				return nil, err
			}
		}
		imported[w.dstRef] = w.oid
	}
	return imported, nil
}

func cleanNamespaceSegment(raw, label string) (string, error) {
	s := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	if s == "" || s == "." || strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
		return "", fmt.Errorf("checkpoint: invalid %s id %q for ref import", label, raw)
	}
	return s, nil
}

func isHexOID(s string) bool { _, err := hex.DecodeString(s); return err == nil && (len(s) == 40 || len(s) == 64) }

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

func copyCandidateGitObjects(storeDir, targetObjectsDir string) error {
	if strings.TrimSpace(storeDir) == "" {
		return nil
	}
	parent := filepath.Dir(storeDir)
	for _, srcObjects := range []string{
		filepath.Join(storeDir, "objects"), filepath.Join(storeDir, "git", "objects"),
		filepath.Join(parent, ".lycaon-private-git", "objects"), filepath.Join(parent, ".git", "objects"),
	} {
		if st, err := os.Stat(srcObjects); err != nil || !st.IsDir() || filepath.Clean(srcObjects) == filepath.Clean(targetObjectsDir) {
			continue
		}
		err := filepath.WalkDir(srcObjects, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			rel, rErr := filepath.Rel(srcObjects, path)
			if rErr != nil || filepath.ToSlash(rel) == "info/alternates" {
				return rErr
			}
			dst := filepath.Join(targetObjectsDir, rel)
			if _, statErr := os.Stat(dst); statErr == nil {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			return writeAtomicJSONOrText(filepath.Dir(dst), filepath.Base(dst), raw, 0o444)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyReachableGitObject(objectsDir, oid string, seen map[string]bool) error {
	if seen[oid] {
		return nil
	}
	f, err := os.Open(filepath.Join(objectsDir, oid[:2], oid[2:]))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return err
	}
	if (len(oid) == 40 && hexObjSHA1(raw) != oid) || (len(oid) == 64 && hexObjSHA256(raw) != oid) {
		return fmt.Errorf("digest mismatch for loose object %s", oid)
	}
	nul := bytes.IndexByte(raw, 0)
	hdrParts := strings.SplitN(string(raw[:max(0, nul)]), " ", 2)
	if nul <= 0 || len(hdrParts) != 2 {
		return fmt.Errorf("malformed loose object header for %s", oid)
	}
	if size, sErr := strconv.Atoi(hdrParts[1]); sErr != nil || size != len(raw[nul+1:]) {
		return fmt.Errorf("size mismatch for %s", oid)
	}
	seen[oid] = true
	body := raw[nul+1:]
	switch hdrParts[0] {
	case "blob":
		return nil
	case "commit", "tag":
		return verifyCommitOrTagBody(objectsDir, body, hdrParts[0] == "commit", seen)
	case "tree":
		for len(body) > 0 {
			hb := len(oid) / 2
			n := bytes.IndexByte(body, 0)
			if n <= 0 || n+1+hb > len(body) {
				return fmt.Errorf("malformed tree entry")
			}
			if err := verifyReachableGitObject(objectsDir, hex.EncodeToString(body[n+1:n+1+hb]), seen); err != nil {
				return err
			}
			body = body[n+1+hb:]
		}
		return nil
	default:
		return fmt.Errorf("unsupported git object type %q", hdrParts[0])
	}
}

func hexObjSHA1(b []byte) string   { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
func hexObjSHA256(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func verifyCommitOrTagBody(objectsDir string, body []byte, isCommit bool, seen map[string]bool) error {
	foundPrimary := false
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			break
		}
		fields := strings.Fields(line)
		if isCommit && len(fields) == 2 && (fields[0] == "tree" || fields[0] == "parent") {
			foundPrimary = foundPrimary || fields[0] == "tree"
			if err := verifyReachableGitObject(objectsDir, fields[1], seen); err != nil {
				return err
			}
		} else if !isCommit && len(fields) == 2 && fields[0] == "object" {
			foundPrimary = true
			if err := verifyReachableGitObject(objectsDir, fields[1], seen); err != nil {
				return err
			}
		}
	}
	if !foundPrimary {
		return fmt.Errorf("missing required target object header")
	}
	return nil
}
