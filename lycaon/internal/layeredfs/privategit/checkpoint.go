package privategit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// FilterPromotableChanges removes .git metadata paths from a view's change list
// so .git metadata is never promoted as ordinary files (#1671).
func FilterPromotableChanges(changes []layeredfs.ChangeEntry) []layeredfs.ChangeEntry {
	out := make([]layeredfs.ChangeEntry, 0, len(changes))
	for _, ce := range changes {
		if _, _, isGit := splitGitRelPath(ce.Path.Rel); isGit {
			continue
		}
		out = append(out, ce)
	}
	return out
}

// CaptureCheckpointGitState captures a coherent CheckpointGitState (HEAD, detached HEAD,
// refs, deleted refs, tags, stashes, index, config) and imports any new private objects
// into the engine-retained object store under a checkpoint lease (#1671).
func (m *Manager) CaptureCheckpointGitState(
	ctx context.Context,
	viewID, checkpointID string,
) (*CheckpointGitState, error) {
	return captureCheckpointGitState(ctx, m, viewID, checkpointID)
}

func captureCheckpointGitState(
	ctx context.Context,
	m *Manager,
	viewID, checkpointID string,
) (*CheckpointGitState, error) {
	if strings.TrimSpace(checkpointID) == "" {
		return nil, fmt.Errorf("privategit: checkpointID is required")
	}
	view, err := getManagedView(m, viewID)
	if err != nil {
		return nil, err
	}

	if view.core != nil {
		tok, bErr := view.core.BeginCheckpointBarrier(ctx)
		if bErr == nil {
			defer func() { _ = view.core.EndCheckpointBarrier(tok) }()
		} else if !errors.Is(bErr, layeredfs.ErrBarrierActive) {
			return nil, bErr
		}
	}

	if err := ensureViewRepoInitialized(ctx, m, view, "."); err != nil {
		return nil, err
	}
	if err := syncViewGitOverlay(view); err != nil {
		return nil, err
	}

	view.mu.Lock()
	repo := view.repos["."]
	workerID := view.workerID
	subHeads := make(map[string]string)
	var subErr error
	for rel, sr := range view.repos {
		if rel != "." && sr != nil && sr.privateGitDir != "" {
			if _, hOID, _, hErr := resolveHeadState(sr.privateGitDir); hErr != nil {
				subErr = hErr
				break
			} else if hOID != "" {
				subHeads[rel] = hOID
			}
		}
	}
	view.mu.Unlock()
	if subErr != nil {
		return nil, subErr
	}
	if repo == nil {
		return nil, layeredfs.ErrGitPending
	}
	if m.cfg.FailObjectRetentionHook != nil {
		if err := m.cfg.FailObjectRetentionHook(viewID, "."); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorruptObjectStore, err)
		}
	}

	headSym, headOID, detached, err := resolveHeadState(repo.privateGitDir)
	if err != nil {
		return nil, err
	}

	tipOIDs, currentRefs, err := collectCapturedRefAndIndexOIDs(ctx, repo.privateGitDir)
	if err != nil {
		return nil, err
	}
	stashes, err := collectStashEntries(ctx, repo.worktreeDir, repo.privateGitDir)
	if err != nil {
		return nil, err
	}
	for _, st := range stashes {
		tipOIDs = append(tipOIDs, st.OID)
	}
	if headOID != "" {
		tipOIDs = append(tipOIDs, headOID)
	}

	privObjDir := filepath.Join(repo.privateGitDir, "objects")
	impMetrics, reachableOIDs, err := m.store.ImportRepositoryObjects(ctx, []string{privObjDir}, tipOIDs)
	if err != nil {
		return nil, err
	}
	leaseID, err := m.store.RetainForCheckpoint(checkpointID, reachableOIDs)
	if err != nil {
		return nil, err
	}

	branches := make(map[string]string)
	tags := make(map[string]string)
	for ref, oid := range currentRefs {
		switch {
		case strings.HasPrefix(ref, "refs/tags/"):
			tags[ref] = oid
		case strings.HasPrefix(ref, "refs/stash"):
			continue
		default:
			branches[ref] = oid
		}
	}

	var deletedRefs []string
	for initRef := range repo.initialRefs {
		if _, exists := currentRefs[initRef]; !exists {
			deletedRefs = append(deletedRefs, initRef)
		}
	}
	sort.Strings(deletedRefs)

	initCopy := make(map[string]string, len(repo.initialRefs))
	for k, v := range repo.initialRefs {
		initCopy[k] = v
	}

	idxBytes, idxErr := os.ReadFile(filepath.Join(repo.privateGitDir, "index"))
	if idxErr != nil && !errors.Is(idxErr, os.ErrNotExist) {
		return nil, idxErr
	}
	cfgBytes, cfgErr := os.ReadFile(filepath.Join(repo.privateGitDir, "config"))
	if cfgErr != nil && !errors.Is(cfgErr, os.ErrNotExist) {
		return nil, cfgErr
	}

	cp := &CheckpointGitState{
		ViewID:       viewID,
		WorkerID:     workerID,
		CheckpointID: checkpointID,
		HeadSymbolic: headSym,
		HeadOID:      headOID,
		DetachedHead: detached,
		Refs:         branches,
		InitialRefs:  initCopy,
		DeletedRefs:  deletedRefs,
		Tags:         tags,
		Stashes:      stashes,
		Artifact: CheckpointGitArtifacts{
			IndexBytes:      idxBytes,
			ConfigBytes:     cfgBytes,
			LeaseID:         leaseID,
			ReachableOIDs:   reachableOIDs,
			ImportedObjects: impMetrics.ObjectsImported + impMetrics.ObjectsReused,
			SubmoduleHeads:  subHeads,
		},
	}

	m.mu.Lock()
	m.checkpoints[checkpointID] = cp
	m.mu.Unlock()
	return cp, nil
}

// VerifyCheckpointHistory verifies that all objects referenced by a retained checkpoint
// remain intact and reachable in the engine-retained object store even if the source
// view was released or the trunk ran `git gc --prune=now` (#1671).
func (m *Manager) VerifyCheckpointHistory(ctx context.Context, cp *CheckpointGitState) error {
	if cp == nil {
		return fmt.Errorf("privategit: checkpoint is nil")
	}
	var tips []string
	if cp.HeadOID != "" {
		tips = append(tips, cp.HeadOID)
	}
	for _, oid := range cp.Refs {
		tips = append(tips, oid)
	}
	for _, oid := range cp.Tags {
		tips = append(tips, oid)
	}
	for _, st := range cp.Stashes {
		tips = append(tips, st.OID)
	}
	m.store.mu.Lock()
	_, err := verifyStoreConnectivityForStore(ctx, m.store, m.store.ObjectsDir(), tips)
	m.store.mu.Unlock()
	return err
}

func resolveHeadState(gitDir string) (symbolic string, oid string, detached bool, err error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", "", false, err
	}
	headStr := strings.TrimSpace(string(raw))
	if strings.HasPrefix(headStr, "ref:") {
		sym := strings.TrimSpace(strings.TrimPrefix(headStr, "ref:"))
		resolved := resolveRefInGitDir(gitDir, sym)
		return sym, resolved, false, nil
	}
	if len(headStr) == 40 && isHexString(headStr) {
		return "", strings.ToLower(headStr), true, nil
	}
	return "", "", false, fmt.Errorf("privategit: invalid HEAD %q", headStr)
}

func resolveRefInGitDir(gitDir, refName string) string {
	loose := filepath.Join(gitDir, filepath.FromSlash(refName))
	if raw, err := os.ReadFile(loose); err == nil {
		val := strings.TrimSpace(string(raw))
		if len(val) == 40 && isHexString(val) {
			return strings.ToLower(val)
		}
	}
	if packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) >= 2 && fields[1] == refName && len(fields[0]) == 40 && isHexString(fields[0]) {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

func collectStashEntries(ctx context.Context, _ string, gitDir string) ([]StashEntry, error) {
	oid := resolveRefInGitDir(gitDir, "refs/stash")
	if oid == "" {
		return nil, nil
	}
	out, code, err := gitexec.Run(ctx, filepath.Dir(gitDir), []string{"--git-dir=" + gitDir, "log", "-g", "--format=%H|%gd|%gs", "refs/stash"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	})
	if err != nil || code != 0 {
		return nil, fmt.Errorf("privategit: collectStashEntries (code %d): %w: %s", code, err, strings.TrimSpace(string(out)))
	}
	var stashes []StashEntry
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		parts := strings.SplitN(s, "|", 3)
		if len(parts) >= 1 && len(parts[0]) == 40 && isHexString(parts[0]) {
			ref := fmt.Sprintf("stash@{%d}", len(stashes))
			if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
				ref = strings.TrimSpace(parts[1])
			}
			msg := "stash"
			if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
				msg = strings.TrimSpace(parts[2])
			}
			stashes = append(stashes, StashEntry{
				Ref:     ref,
				OID:     strings.ToLower(parts[0]),
				Message: msg,
			})
		}
	}
	if len(stashes) == 0 {
		return []StashEntry{{Ref: "stash@{0}", OID: oid, Message: "stash"}}, nil
	}
	return stashes, nil
}

// ExportCheckpointObjects packs the objects a checkpoint's refs add beyond what the view started
// with into dst, so promotion can import them without the private repository.
func (m *Manager) ExportCheckpointObjects(ctx context.Context, viewID string, state *CheckpointGitState, dst string) error {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return err
	}
	if err := ensureViewRepoInitialized(ctx, m, view, "."); err != nil {
		return err
	}
	if err := syncViewGitOverlay(view); err != nil {
		return err
	}
	view.mu.Lock()
	repo := view.repos["."]
	view.mu.Unlock()
	if repo == nil {
		return layeredfs.ErrGitPending
	}
	var revisions strings.Builder
	addTips := func(oids map[string]string) {
		for _, oid := range oids {
			fmt.Fprintln(&revisions, oid)
		}
	}
	addTips(state.Refs)
	addTips(state.Tags)
	if state.HeadOID != "" {
		fmt.Fprintln(&revisions, state.HeadOID)
	}
	for _, stash := range state.Stashes {
		fmt.Fprintln(&revisions, stash.OID)
	}
	if revisions.Len() == 0 {
		return nil
	}
	for _, oid := range state.InitialRefs {
		fmt.Fprintf(&revisions, "^%s\n", oid)
	}
	if err := os.MkdirAll(filepath.Join(dst, "pack"), 0o700); err != nil {
		return err
	}
	args := []string{"--git-dir=" + repo.privateGitDir, "pack-objects", "--revs", "--quiet", filepath.Join(dst, "pack", "pack")}
	output, code, err := gitexec.Run(ctx, repo.privateGitDir, args, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(revisions.String())})
	if err != nil || code != 0 {
		return errors.Join(err, fmt.Errorf("privategit: export checkpoint objects (exit %d): %s", code, strings.TrimSpace(string(output))))
	}
	return nil
}
