package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// GitRefImportRecord durably records checkpoint ref import intent and expected prior OIDs before ref mutation.
type GitRefImportRecord struct {
	CheckpointID string            `json:"checkpoint_id,omitempty"`
	WorkerID     string            `json:"worker_id"`
	PlannedRefs  map[string]string `json:"planned_refs"`
	ExpectedOld  map[string]string `json:"expected_old,omitempty"`
	Completed    bool              `json:"completed"`
}

// ResolveGitDirs resolves the worktree gitDir and shared commonGitDir for repoRoot,
// rejecting symlinks and requiring external gitdir/commondir paths to reside under authorizedDirs.
func ResolveGitDirs(repoRoot string, authorizedDirs ...string) (gitDir, commonGitDir string, err error) {
	if repoRoot = filepath.Clean(strings.TrimSpace(repoRoot)); repoRoot == "" || repoRoot == "." {
		return "", "", fmt.Errorf("checkpoint: repository root is required")
	}
	if err := validateResolvedGitDir(repoRoot); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrUnauthorizedGitDir, err)
	}
	dotGit := filepath.Join(repoRoot, ".git")
	st, err := os.Lstat(dotGit)
	if err != nil || st.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("%w: resolve .git at %s (%v)", ErrUnauthorizedGitDir, dotGit, err)
	}
	if st.IsDir() {
		gitDir = dotGit
	} else if st.Mode().IsRegular() {
		raw, rErr := readBoundedFile(dotGit, 4096)
		line := strings.TrimSpace(string(raw))
		if rErr != nil || !strings.HasPrefix(line, "gitdir:") || strings.ContainsAny(line, "\r\n\x00") {
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
	if err := validateResolvedGitDir(gitDir); err != nil {
		return "", "", err
	}
	if commonGitDir, err = resolveCommonGitDir(gitDir); err != nil {
		return "", "", err
	}
	allowed := append([]string{repoRoot}, authorizedDirs...)
	if !isPathWithinAuthorized(gitDir, allowed) || !isPathWithinAuthorized(commonGitDir, allowed) {
		return "", "", fmt.Errorf("%w: gitdir=%s commondir=%s", ErrUnauthorizedGitDir, gitDir, commonGitDir)
	}
	return gitDir, commonGitDir, nil
}

func resolveCommonGitDir(gitDir string) (string, error) {
	commonGitDir, commonFile := gitDir, filepath.Join(gitDir, "commondir")
	if cst, err := os.Lstat(commonFile); err == nil {
		if cst.Mode()&os.ModeSymlink != 0 || !cst.Mode().IsRegular() {
			return "", fmt.Errorf("%w: invalid commondir entry at %s", ErrUnauthorizedGitDir, commonFile)
		}
		rawCommon, rErr := readBoundedFile(commonFile, 4096)
		if rErr != nil {
			return "", rErr
		}
		if rel := strings.TrimSpace(string(rawCommon)); rel != "" {
			if strings.ContainsAny(rel, "\r\n\x00") {
				return "", fmt.Errorf("checkpoint: invalid commondir content at %s", commonFile)
			}
			if !filepath.IsAbs(rel) {
				rel = filepath.Join(gitDir, rel)
			}
			commonGitDir = filepath.Clean(rel)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return commonGitDir, validateResolvedGitDir(commonGitDir)
}

func validateResolvedGitDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("checkpoint: git directory %s unavailable: %w", dir, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("%w: invalid git directory %s", ErrUnauthorizedGitDir, dir)
	}
	return nil
}

func isPathWithinAuthorized(target string, allowed []string) bool {
	absTarget, err := filepath.Abs(target)
	realTarget, rErr := filepath.EvalSymlinks(absTarget)
	if err != nil || rErr != nil {
		return false
	}
	for _, root := range allowed {
		absRoot, aErr := filepath.Abs(strings.TrimSpace(root))
		realRoot, eErr := filepath.EvalSymlinks(absRoot)
		if strings.TrimSpace(root) != "" && aErr == nil && eErr == nil && (realTarget == realRoot || strings.HasPrefix(realTarget, realRoot+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// ValidateGitRefSegment validates a single path component of a Git ref name.
func ValidateGitRefSegment(seg, label string) error {
	if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, ".lock") {
		return fmt.Errorf("checkpoint: invalid %s segment %q", label, seg)
	}
	if strings.Contains(seg, "..") || strings.Contains(seg, "@{") {
		return fmt.Errorf("checkpoint: forbidden sequence in %s segment %q", label, seg)
	}
	for _, r := range seg {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r) {
			return fmt.Errorf("checkpoint: illegal character in %s segment %q", label, seg)
		}
	}
	return nil
}

// ValidateGitRefName strictly validates a Git reference name and returns its normalized suffix under refs/.
func ValidateGitRefName(ref string) (string, error) {
	if ref == "" || strings.TrimSpace(ref) != ref || strings.Contains(ref, `\`) || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "//") {
		return "", fmt.Errorf("checkpoint: invalid git ref name %q", ref)
	}
	trimmed := strings.TrimPrefix(ref, "refs/")
	if trimmed == "" {
		return "", fmt.Errorf("checkpoint: empty git ref name %q", ref)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if err := ValidateGitRefSegment(seg, "ref"); err != nil {
			return "", err
		}
	}
	return trimmed, nil
}

type plannedRefWrite struct{ dstRef, oid string }

// ImportAndValidateCheckpointGitRefs imports retained Git objects from the checkpoint store,
// validates reachable object closure, records durable import intent, and updates refs via git update-ref CAS.
func ImportAndValidateCheckpointGitRefs(
	ctx context.Context, storeDir, checkpointID, workerID string, git PrivateGitSnapshot,
	targetRoots map[string]string, authorizedGitDirs ...string,
) (map[string]string, error) {
	if len(git.Refs) == 0 {
		return nil, nil
	}
	cleanWorker, cleanCheckpoint := strings.TrimSpace(workerID), strings.TrimSpace(checkpointID)
	if err := ValidateGitRefSegment(cleanWorker, "worker"); err != nil {
		return nil, err
	}
	if cleanCheckpoint != "" {
		if err := ValidateGitRefSegment(cleanCheckpoint, "checkpoint"); err != nil {
			return nil, err
		}
	}
	primaryDir := selectPrimaryRootDir(targetRoots)
	gitDir, commonGitDir, err := ResolveGitDirs(primaryDir, authorizedGitDirs...)
	if err != nil {
		return nil, err
	}
	targetObjectsDir := filepath.Join(commonGitDir, "objects")
	if err := errors.Join(ensureMinimalGitDirLayout(gitDir, commonGitDir), copyCandidateGitObjects(storeDir, cleanCheckpoint, targetObjectsDir)); err != nil {
		return nil, err
	}
	refNames := make([]string, 0, len(git.Refs))
	for name := range git.Refs {
		refNames = append(refNames, name)
	}
	sort.Strings(refNames)

	verifiedOIDs := make(map[string]bool, len(refNames)*4)
	var writes []plannedRefWrite
	for _, refName := range refNames {
		cleanRel, nameErr := ValidateGitRefName(refName)
		if nameErr != nil {
			return nil, nameErr
		}
		oid := strings.ToLower(strings.TrimSpace(git.Refs[refName]))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid git oid %q for ref %q", oid, refName)
		}
		if err := verifyReachableGitObject(ctx, primaryDir, commonGitDir, targetObjectsDir, oid, verifiedOIDs); err != nil {
			return nil, fmt.Errorf("checkpoint: unreachable or corrupt git object for ref %s (%s): %w", refName, oid, err)
		}
		writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/workers/" + cleanWorker + "/" + cleanRel, oid: oid})
		if cleanCheckpoint != "" {
			writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/checkpoints/" + cleanCheckpoint + "/" + cleanRel, oid: oid})
		}
	}
	return applyCheckpointRefTransaction(ctx, primaryDir, gitDir, commonGitDir, storeDir, cleanCheckpoint, cleanWorker, writes)
}

func applyCheckpointRefTransaction(
	ctx context.Context, primaryDir, gitDir, commonGitDir, storeDir, cleanCheckpoint, cleanWorker string, writes []plannedRefWrite,
) (map[string]string, error) {
	planned := make(map[string]string, len(writes))
	for _, w := range writes {
		planned[w.dstRef] = w.oid
	}
	intentName := "git-import-" + cleanWorker + "--" + cleanCheckpoint + ".json"
	if strings.TrimSpace(storeDir) != "" {
		if raw, err := os.ReadFile(filepath.Join(storeDir, "promotions", intentName)); err == nil {
			var existing GitRefImportRecord
			if uErr := json.Unmarshal(raw, &existing); uErr != nil {
				return nil, uErr
			}
			if existing.WorkerID != cleanWorker || !maps.Equal(existing.PlannedRefs, planned) {
				return nil, fmt.Errorf("%w: conflicting durable ref import intent", ErrCheckpointRefConflict)
			}
		}
	}
	expectedOld := make(map[string]string, len(writes))
	var stdinBuf strings.Builder
	for _, w := range writes {
		curOID := resolveExistingGitRef(commonGitDir, gitDir, w.dstRef)
		if curOID != "" && curOID != w.oid {
			return nil, fmt.Errorf("%w: %s has %s, want %s", ErrCheckpointRefConflict, w.dstRef, curOID, w.oid)
		}
		expectedOld[w.dstRef] = curOID
		op := "create"
		if curOID == w.oid {
			op = "verify"
		}
		_, _ = fmt.Fprintf(&stdinBuf, "%s %s %s\n", op, w.dstRef, w.oid)
	}
	rec := GitRefImportRecord{CheckpointID: cleanCheckpoint, WorkerID: cleanWorker, PlannedRefs: planned, ExpectedOld: expectedOld}
	if err := saveGitRefImportRecord(storeDir, intentName, rec); err != nil {
		return nil, err
	}
	out, code, err := gitexec.Run(ctx, primaryDir, []string{"--git-dir=" + commonGitDir, "update-ref", "--stdin"}, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(stdinBuf.String())})
	if err != nil || code != 0 {
		return nil, fmt.Errorf("%w: update-ref failed: %v (%s)", ErrCheckpointRefConflict, err, strings.TrimSpace(string(out)))
	}
	rec.Completed = true
	return planned, saveGitRefImportRecord(storeDir, intentName, rec)
}

func saveGitRefImportRecord(storeDir, intentName string, rec GitRefImportRecord) error {
	if strings.TrimSpace(storeDir) == "" {
		return nil
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicJSONOrText(filepath.Join(storeDir, "promotions"), intentName, raw, 0o600)
}

func resolveExistingGitRef(commonGitDir, gitDir, dstRef string) string {
	for _, baseDir := range []string{commonGitDir, gitDir} {
		if raw, err := readBoundedFile(filepath.Join(baseDir, filepath.FromSlash(dstRef)), 256); err == nil {
			if oid := strings.ToLower(strings.TrimSpace(string(raw))); isHexOID(oid) {
				return oid
			}
		}
	}
	for _, baseDir := range []string{commonGitDir, gitDir} {
		if raw, err := readBoundedFile(filepath.Join(baseDir, "packed-refs"), 8<<20); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && !strings.HasPrefix(f[0], "^") && f[1] == dstRef && isHexOID(strings.ToLower(f[0])) {
					return strings.ToLower(f[0])
				}
			}
		}
	}
	return ""
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
