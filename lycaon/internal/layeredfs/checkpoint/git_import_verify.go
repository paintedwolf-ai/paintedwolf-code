package checkpoint

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 is the object identifier in the admitted Git repository format.
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
)

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
	h := sha1.New() // #nosec G401 -- Verify standard Git object identifiers, not security signatures.
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
