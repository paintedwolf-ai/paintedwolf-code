package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
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
		return "", "", fmt.Errorf("%w: %w", ErrUnauthorizedGitDir, err)
	}
	dotGit := filepath.Join(repoRoot, ".git")
	st, err := os.Lstat(dotGit)
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve .git at %s: %w", ErrUnauthorizedGitDir, dotGit, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("%w: .git is a symbolic link at %s", ErrUnauthorizedGitDir, dotGit)
	}
	if st.IsDir() {
		gitDir = dotGit
	} else if st.Mode().IsRegular() {
		raw, rErr := readBoundedFile(dotGit, 4096)
		line := strings.TrimSpace(string(raw))
		if rErr != nil || !strings.HasPrefix(line, "gitdir:") || strings.ContainsAny(line, "\r\n\x00") {
			return "", "", fmt.Errorf("checkpoint: invalid .git file at %s", dotGit)
		}
		if gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:")); !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repoRoot, gitDir)
		}
		gitDir = filepath.Clean(gitDir)
		if err := validateResolvedGitDir(gitDir); err != nil {
			return "", "", err
		}
	} else {
		return "", "", fmt.Errorf("checkpoint: unsupported .git entry at %s", dotGit)
	}
	if commonGitDir, err = resolveCommonGitDir(gitDir); err != nil {
		return "", "", err
	}
	if gitDir == dotGit && commonGitDir == gitDir {
		return gitDir, commonGitDir, nil
	}
	allowed := append([]string{repoRoot}, authorizedDirs...)
	if !isPathWithinAuthorized(gitDir, allowed) || (commonGitDir != gitDir && !isPathWithinAuthorized(commonGitDir, allowed)) {
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
	if commonGitDir == gitDir {
		return commonGitDir, nil
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

// ImportAndValidateCheckpointGitRefs imports each root's retained Git objects from the checkpoint
// store into that root's repository, validates reachable object closure, records durable import
// intent, and updates refs via git update-ref CAS. A non-primary root's imported refs are keyed
// "<root ID>:<ref>". A root with no repository has nothing to import them into; its objects stay
// retained with the checkpoint.
func ImportAndValidateCheckpointGitRefs(
	ctx context.Context, storeDir, checkpointID, workerID string, git PrivateGitSnapshot,
	targetRoots map[string]string, authorizedGitDirs ...string,
) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !git.HasRefs() {
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
	request := rootRefImport{
		storeDir: storeDir, checkpointID: cleanCheckpoint, workerID: cleanWorker,
		objectCheckpoints: git.ObjectCheckpoints, authorizedGitDirs: authorizedGitDirs,
	}
	imported := map[string]string{}
	if len(git.Refs) > 0 {
		primaryDir := selectPrimaryRootDir(targetRoots)
		if repositoryPresent(primaryDir) {
			request.repoRoot, request.refs = primaryDir, git.Refs
			planned, err := importRootRefs(ctx, request)
			if err != nil {
				return nil, err
			}
			maps.Copy(imported, planned)
		}
	}
	for _, rootID := range slices.Sorted(maps.Keys(git.Roots)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := strings.TrimSpace(targetRoots[rootID])
		if dir == "" || !safeComponent(rootID) {
			return nil, fmt.Errorf("checkpoint: git refs name unknown root %q", rootID)
		}
		if len(git.Roots[rootID].Refs) == 0 || !repositoryPresent(dir) {
			continue
		}
		request.repoRoot, request.rootID, request.refs = dir, rootID, git.Roots[rootID].Refs
		planned, err := importRootRefs(ctx, request)
		if err != nil {
			return nil, err
		}
		for ref, oid := range planned {
			imported[rootID+":"+ref] = oid
		}
	}
	return imported, nil
}

// repositoryPresent reports whether a target root has any Git entry to resolve. Anything present
// goes on to full validation; only a plain absence means there is no repository.
func repositoryPresent(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return !errors.Is(err, os.ErrNotExist)
}

type rootRefImport struct {
	storeDir, checkpointID, workerID string
	repoRoot, rootID                 string
	refs                             map[string]string
	objectCheckpoints                []string
	authorizedGitDirs                []string
}

func importRootRefs(ctx context.Context, req rootRefImport) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gitDir, commonGitDir, err := ResolveGitDirs(req.repoRoot, req.authorizedGitDirs...)
	if err != nil {
		return nil, err
	}
	targetObjectsDir := filepath.Join(commonGitDir, "objects")
	if err := ensureMinimalGitDirLayout(gitDir, commonGitDir); err != nil {
		return nil, err
	}
	source, cleanupSource, err := openCandidateGitObjectSource(
		ctx, req.storeDir, req.checkpointID, req.rootID, req.objectCheckpoints,
		req.repoRoot, gitDir, commonGitDir, targetObjectsDir,
	)
	if err != nil {
		return nil, err
	}
	defer cleanupSource()
	refNames := make([]string, 0, len(req.refs))
	for name := range req.refs {
		refNames = append(refNames, name)
	}
	sort.Strings(refNames)
	var (
		tipOIDs []string
		writes  []plannedRefWrite
	)
	for _, refName := range refNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanRel, nameErr := ValidateGitRefName(refName)
		if nameErr != nil {
			return nil, nameErr
		}
		oid := strings.ToLower(strings.TrimSpace(req.refs[refName]))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid git oid %q for ref %q", oid, refName)
		}
		tipOIDs = append(tipOIDs, oid)
		writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/workers/" + req.workerID + "/" + cleanRel, oid: oid})
		if req.checkpointID != "" {
			writes = append(writes, plannedRefWrite{dstRef: "refs/lycaon/checkpoints/" + req.checkpointID + "/" + cleanRel, oid: oid})
		}
	}
	if err := verifyAndPublishReachableGitGraph(ctx, source, tipOIDs); err != nil {
		return nil, fmt.Errorf("checkpoint: unreachable or corrupt git object: %w", err)
	}
	return applyCheckpointRefTransaction(ctx, req.repoRoot, gitDir, commonGitDir, req.storeDir, req.checkpointID, req.workerID, req.rootID, writes)
}

func applyCheckpointRefTransaction(
	ctx context.Context, _, gitDir, commonGitDir, storeDir, cleanCheckpoint, cleanWorker, rootID string, writes []plannedRefWrite,
) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planned := make(map[string]string, len(writes))
	for _, w := range writes {
		planned[w.dstRef] = w.oid
	}
	intentName := gitImportRecordName(cleanWorker, cleanCheckpoint, rootID)
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
	_, lycaonErr := os.Lstat(filepath.Join(commonGitDir, "refs", "lycaon"))
	noLooseLycaon := errors.Is(lycaonErr, os.ErrNotExist) && filepath.Clean(gitDir) == filepath.Clean(commonGitDir)
	packed := loadPackedRefsMap(commonGitDir, gitDir)
	expectedOld := make(map[string]string, len(writes))
	for _, w := range writes {
		curOID := packed[w.dstRef]
		if !noLooseLycaon {
			curOID = resolveExistingGitRefWithPacked(commonGitDir, gitDir, w.dstRef, packed)
		}
		if curOID != "" && curOID != w.oid {
			return nil, fmt.Errorf("%w: %s has %s, want %s", ErrCheckpointRefConflict, w.dstRef, curOID, w.oid)
		}
		expectedOld[w.dstRef] = curOID
	}
	rec := GitRefImportRecord{CheckpointID: cleanCheckpoint, WorkerID: cleanWorker, PlannedRefs: planned, ExpectedOld: expectedOld}
	if err := saveGitRefImportRecord(storeDir, intentName, rec); err != nil {
		return nil, err
	}
	if err := applyLooseGitRefCAS(ctx, commonGitDir, gitDir, writes, expectedOld, packed, noLooseLycaon); err != nil {
		return nil, err
	}
	rec.Completed = true
	return planned, saveGitRefImportRecord(storeDir, intentName, rec)
}

func applyLooseGitRefCAS(ctx context.Context, commonGitDir, gitDir string, writes []plannedRefWrite, expectedOld, packed map[string]string, noLooseLycaon bool) error {
	sorted := slices.Clone(writes)
	slices.SortFunc(sorted, func(a, b plannedRefWrite) int { return strings.Compare(a.dstRef, b.dstRef) })
	type refLock struct {
		w        plannedRefWrite
		refPath  string
		lockPath string
		file     *os.File
	}
	locks := make([]refLock, 0, len(sorted))
	cleanup := func() {
		for _, lk := range locks {
			if lk.file != nil {
				_ = lk.file.Close()
			}
			if lk.lockPath != "" {
				_ = os.Remove(lk.lockPath)
			}
		}
	}
	madeDirs := make(map[string]bool, len(sorted)*4)
	for _, w := range sorted {
		if err := ctx.Err(); err != nil {
			cleanup()
			return err
		}
		refPath := filepath.Join(commonGitDir, filepath.FromSlash(w.dstRef))
		if err := ensureRefParentDirs(commonGitDir, w.dstRef, madeDirs); err != nil {
			cleanup()
			return err
		}
		lockPath := refPath + ".lock"
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			cleanup()
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: ref lock busy for %s", ErrCheckpointRefConflict, w.dstRef)
			}
			return err
		}
		locks = append(locks, refLock{w: w, refPath: refPath, lockPath: lockPath, file: f})
	}
	if !noLooseLycaon {
		for _, lk := range locks {
			curOID := resolveExistingGitRefWithPacked(commonGitDir, gitDir, lk.w.dstRef, packed)
			if curOID != expectedOld[lk.w.dstRef] && curOID != lk.w.oid {
				cleanup()
				return fmt.Errorf("%w: %s has %s, want %s", ErrCheckpointRefConflict, lk.w.dstRef, curOID, lk.w.oid)
			}
		}
	}
	syncedDirs := make(map[string]bool, len(locks))
	for i := range locks {
		lk := &locks[i]
		if _, err := lk.file.WriteString(lk.w.oid + "\n"); err != nil {
			cleanup()
			return err
		}
		if err := errors.Join(syncObjectFileFn(lk.file), closeObjectFileFn(lk.file)); err != nil {
			lk.file = nil
			cleanup()
			return err
		}
		lk.file = nil
		dir := filepath.Dir(lk.refPath)
		if err := fseffect.Rename(dir, filepath.Base(lk.lockPath), filepath.Base(lk.refPath)); err != nil {
			cleanup()
			return err
		}
		lk.lockPath = ""
		if !syncedDirs[dir] {
			syncedDirs[dir] = true
			if err := syncDir(dir); err != nil {
				cleanup()
				return err
			}
		}
	}
	return nil
}

func ensureRefParentDirs(commonGitDir, dstRef string, madeDirs map[string]bool) error {
	parts := strings.Split(strings.TrimPrefix(dstRef, "/"), "/")
	cur := commonGitDir
	for _, seg := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, seg)
		if madeDirs[cur] {
			continue
		}
		if seg != "refs" {
			if err := os.Mkdir(cur, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		madeDirs[cur] = true
	}
	return nil
}

func gitImportRecordName(workerID, checkpointID, rootID string) string {
	if rootID == "" {
		return "git-import-" + workerID + "--" + checkpointID + ".json"
	}
	return "git-import-" + workerID + "--" + checkpointID + "--" + rootID + ".json"
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

func loadPackedRefsMap(commonGitDir, gitDir string) map[string]string {
	dirs := []string{commonGitDir}
	if filepath.Clean(gitDir) != filepath.Clean(commonGitDir) {
		dirs = []string{gitDir, commonGitDir}
	}
	out := make(map[string]string)
	for _, baseDir := range dirs {
		if raw, err := readBoundedFile(filepath.Join(baseDir, "packed-refs"), 8<<20); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && !strings.HasPrefix(f[0], "^") && isHexOID(strings.ToLower(f[0])) {
					out[f[1]] = strings.ToLower(f[0])
				}
			}
		}
	}
	return out
}

func resolveExistingGitRefWithPacked(commonGitDir, gitDir, dstRef string, packed map[string]string) string {
	dirs := []string{commonGitDir}
	if filepath.Clean(gitDir) != filepath.Clean(commonGitDir) {
		dirs = []string{commonGitDir, gitDir}
	}
	for _, baseDir := range dirs {
		if raw, err := readBoundedFile(filepath.Join(baseDir, filepath.FromSlash(dstRef)), 256); err == nil {
			if oid := strings.ToLower(strings.TrimSpace(string(raw))); isHexOID(oid) {
				return oid
			}
		}
	}
	return packed[dstRef]
}

func resolveExistingGitRef(commonGitDir, gitDir, dstRef string) string {
	return resolveExistingGitRefWithPacked(commonGitDir, gitDir, dstRef, loadPackedRefsMap(commonGitDir, gitDir))
}

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
