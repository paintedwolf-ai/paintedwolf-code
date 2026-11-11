package checkpoint

import (
	"context"
	"crypto/sha1" // #nosec G505 -- SHA-1 is the object identifier in the admitted Git repository format.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// ImportAndValidateCheckpointGitRefs publishes each root's new Git objects from the checkpoint store
// into that root's repository over its established history, records durable import intent, and
// updates refs in one update-ref transaction. A non-primary root's imported refs are keyed
// "<root ID>:<ref>". A root with no repository has nothing to import them into; its objects stay
// retained with the checkpoint.
func ImportAndValidateCheckpointGitRefs(
	ctx context.Context, sources GitObjectSources, checkpointID, workerID string, git PrivateGitSnapshot,
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
		storeDir: sources.StoreDir, retained: sources.Retained, checkpointID: cleanCheckpoint, workerID: cleanWorker,
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

func ensureMinimalGitDirLayout(gitDir, commonGitDir string) error {
	existing := make(map[string]bool, 8)
	if entries, err := os.ReadDir(commonGitDir); err == nil {
		for _, d := range entries {
			existing[d.Name()] = true
		}
	}
	for _, name := range []string{"objects", "refs"} {
		if !existing[name] {
			if err := os.MkdirAll(filepath.Join(commonGitDir, name), 0o750); err != nil {
				return err
			}
		}
	}
	ensureFile := func(dir, name, content string) error {
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			return writeAtomicJSONOrText(dir, name, []byte(content), 0o644)
		}
		return nil
	}
	if !existing["config"] {
		if err := ensureFile(commonGitDir, "config", "[core]\n\trepositoryformatversion = 0\n"); err != nil {
			return err
		}
	}
	if !existing["HEAD"] {
		if err := ensureFile(commonGitDir, "HEAD", "ref: refs/heads/main\n"); err != nil {
			return err
		}
	}
	if filepath.Clean(gitDir) != filepath.Clean(commonGitDir) {
		return ensureFile(gitDir, "HEAD", "ref: refs/heads/main\n")
	}
	return nil
}

// GitObjectSources are where a checkpoint's objects may come from: the objects it and its stacked
// parents exported into the store, and the engine's retained stores holding the history they build
// on. Only objects the worker's tips reach over the destination's boundary are taken from either.
type GitObjectSources struct {
	StoreDir string
	Retained []string
}

type rootRefImport struct {
	storeDir, checkpointID, workerID string
	retained                         []string
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
	if err := ensureMinimalGitDirLayout(gitDir, commonGitDir); err != nil {
		return nil, err
	}
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
	pub := gitPublication{repoRoot: req.repoRoot, commonGitDir: commonGitDir, tips: tipOIDs}
	for _, id := range append([]string{req.checkpointID}, req.objectCheckpoints...) {
		if id != "" && strings.TrimSpace(req.storeDir) != "" {
			pub.sourceObjectDirs = append(pub.sourceObjectDirs, CheckpointGitObjectsDir(req.storeDir, id, req.rootID))
		}
	}
	pub.sourceObjectDirs = append(pub.sourceObjectDirs, req.retained...)
	tx, err := planCheckpointRefTransaction(req.storeDir, gitDir, commonGitDir, req.checkpointID, req.workerID, req.rootID, writes)
	if err != nil {
		return nil, err
	}
	if _, err := publishGitObjects(ctx, pub); err != nil {
		return nil, err
	}
	return tx.apply(ctx, req.repoRoot, commonGitDir, req.storeDir)
}

// refTransaction is a checked set of ref writes: each target is absent or already at its object.
type refTransaction struct {
	intentName string
	record     GitRefImportRecord
	commands   string
}

// planCheckpointRefTransaction refuses a write that conflicts with a ref or with a recorded intent before
// any object moves.
func planCheckpointRefTransaction(storeDir, gitDir, commonGitDir, cleanCheckpoint, cleanWorker, rootID string, writes []plannedRefWrite) (refTransaction, error) {
	planned := make(map[string]string, len(writes))
	for _, w := range writes {
		planned[w.dstRef] = w.oid
	}
	intentName := gitImportRecordName(cleanWorker, cleanCheckpoint, rootID)
	if strings.TrimSpace(storeDir) != "" {
		if raw, err := os.ReadFile(filepath.Join(storeDir, "promotions", intentName)); err == nil {
			var existing GitRefImportRecord
			if uErr := json.Unmarshal(raw, &existing); uErr != nil {
				return refTransaction{}, uErr
			}
			if existing.WorkerID != cleanWorker || !maps.Equal(existing.PlannedRefs, planned) {
				return refTransaction{}, fmt.Errorf("%w: conflicting durable ref import intent", ErrCheckpointRefConflict)
			}
		}
	}
	packed := loadPackedRefsMap(commonGitDir, gitDir)
	expectedOld := make(map[string]string, len(writes))
	var commands strings.Builder
	for _, w := range writes {
		curOID := resolveExistingGitRefWithPacked(commonGitDir, gitDir, w.dstRef, packed)
		if curOID != "" && curOID != w.oid {
			return refTransaction{}, fmt.Errorf("%w: %s has %s, want %s", ErrCheckpointRefConflict, w.dstRef, curOID, w.oid)
		}
		expectedOld[w.dstRef] = curOID
		op := "create"
		if curOID == w.oid {
			op = "verify"
		}
		_, _ = fmt.Fprintf(&commands, "%s %s %s\n", op, w.dstRef, w.oid)
	}
	return refTransaction{
		intentName: intentName,
		record:     GitRefImportRecord{CheckpointID: cleanCheckpoint, WorkerID: cleanWorker, PlannedRefs: planned, ExpectedOld: expectedOld},
		commands:   commands.String(),
	}, nil
}

// apply records the intent, then writes every ref in one update-ref transaction, which refuses a ref
// that moved since the plan.
func (tx refTransaction) apply(ctx context.Context, primaryDir, commonGitDir, storeDir string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rec := tx.record
	if err := saveGitRefImportRecord(storeDir, tx.intentName, rec); err != nil {
		return nil, err
	}
	out, code, err := gitexec.Run(ctx, primaryDir, []string{"--git-dir=" + commonGitDir, "update-ref", "--stdin"}, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(tx.commands)})
	if err != nil || code != 0 {
		return nil, errors.Join(err, fmt.Errorf("%w: update-ref failed (exit %d): %s", ErrCheckpointRefConflict, code, strings.TrimSpace(string(out))))
	}
	rec.Completed = true
	return rec.PlannedRefs, saveGitRefImportRecord(storeDir, tx.intentName, rec)
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
