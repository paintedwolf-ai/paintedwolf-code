package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// prepareRefs records each root's ref undo. Only the built-in namespaced importer runs, because its
// refs can be restored by compare-and-swap.
func (txn *Transaction) prepareRefs(m Manifest, targetRoots map[string]string, primaryID string) ([]refUndo, error) {
	if !m.PrivateGit.HasRefs() {
		return nil, nil
	}
	if err := ValidateGitRefSegment(m.WorkerID, "worker"); err != nil {
		return nil, err
	}
	if err := ValidateGitRefSegment(m.CheckpointID, "checkpoint"); err != nil {
		return nil, err
	}
	var undo []refUndo
	if len(m.PrivateGit.Refs) > 0 && repositoryPresent(targetRoots[primaryID]) {
		txn.journal.Checkpoint.PrimaryRootID = primaryID
		refs, err := txn.rootRefUndo(m, primaryID, targetRoots[primaryID], m.PrivateGit.Refs)
		if err != nil {
			return nil, err
		}
		undo = append(undo, refs...)
	}
	for _, rootID := range slices.Sorted(maps.Keys(m.PrivateGit.Roots)) {
		dir := strings.TrimSpace(targetRoots[rootID])
		if dir == "" {
			return nil, fmt.Errorf("checkpoint: git refs name unknown root %q", rootID)
		}
		if !repositoryPresent(dir) {
			continue
		}
		refs, err := txn.rootRefUndo(m, rootID, dir, m.PrivateGit.Roots[rootID].Refs)
		if err != nil {
			return nil, err
		}
		undo = append(undo, refs...)
	}
	return undo, nil
}

func (txn *Transaction) rootRefUndo(m Manifest, rootID, dir string, named map[string]string) ([]refUndo, error) {
	gitDir, common, err := txn.resolveRootGitDirs(rootID, dir)
	if err != nil {
		return nil, err
	}
	if err := txn.recordRootGitDirIdentity(rootID, gitDir, common); err != nil {
		return nil, err
	}
	packed := loadPackedRefsMap(common, gitDir)
	var refs []refUndo
	for _, name := range slices.Sorted(maps.Keys(named)) {
		suffix, err := ValidateGitRefName(name)
		if err != nil {
			return nil, err
		}
		oid := strings.ToLower(strings.TrimSpace(named[name]))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid ref object")
		}
		for _, ref := range []string{"refs/lycaon/workers/" + m.WorkerID + "/" + suffix, "refs/lycaon/checkpoints/" + m.CheckpointID + "/" + suffix} {
			old := resolveExistingGitRefWithPacked(common, gitDir, ref, packed)
			if old != "" && old != oid {
				return nil, ErrCheckpointRefConflict
			}
			refs = append(refs, refUndo{RootID: rootID, Name: ref, Before: old, After: oid})
		}
	}
	return refs, nil
}

// rollbackRefs restores each root's refs in its own repository.
func (txn *Transaction) rollbackRefs(ctx context.Context, refs []refUndo) error {
	for start := 0; start < len(refs); {
		end := start
		for end < len(refs) && refs[end].RootID == refs[start].RootID {
			end++
		}
		if err := txn.rollbackRootRefs(ctx, refs[start].RootID, refs[start:end]); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (txn *Transaction) rollbackRootRefs(ctx context.Context, rootID string, refs []refUndo) error {
	primary := txn.targets.roots[rootID]
	gitDir, common, err := txn.verifyRecordedRootGitDirs(rootID, primary)
	if err != nil {
		return err
	}
	packed := loadPackedRefsMap(common, gitDir)
	var input strings.Builder
	for _, ref := range refs {
		if _, err := ValidateGitRefName(ref.Name); err != nil {
			return err
		}
		if !strings.HasPrefix(ref.Name, "refs/lycaon/") || !isHexOID(ref.After) || (ref.Before != "" && !isHexOID(ref.Before)) {
			return ErrInvalidManifest
		}
		current := resolveExistingGitRefWithPacked(common, gitDir, ref.Name, packed)
		if current == ref.Before {
			continue
		}
		if current != ref.After {
			return fmt.Errorf("%w: imported ref %s diverged", ErrCheckpointRefConflict, ref.Name)
		}
		if ref.Before == "" {
			fmt.Fprintf(&input, "delete %s %s\n", ref.Name, ref.After)
		} else {
			fmt.Fprintf(&input, "update %s %s %s\n", ref.Name, ref.Before, ref.After)
		}
	}
	if input.Len() == 0 {
		return nil
	}
	output, code, err := gitexec.Run(ctx, primary, []string{"--git-dir=" + common, "update-ref", "--stdin"}, gitexec.Opts{Profile: gitexec.ProfileHermetic, Input: []byte(input.String())})
	if err != nil || code != 0 {
		return errors.Join(err, fmt.Errorf("%w: rollback refs failed (exit %d): %s", ErrCheckpointRefConflict, code, strings.TrimSpace(string(output))))
	}
	return nil
}

func (txn *Transaction) resolveRootGitDirs(rootID, dir string) (string, string, error) {
	if auth, ok := txn.cfg.AuthorizedRootGitDirs[rootID]; ok {
		gitDir, common, err := ResolveGitDirs(dir, auth.GitDir, auth.CommonDir)
		if err != nil {
			return "", "", err
		}
		if !sameCanonicalPath(gitDir, auth.GitDir) || !sameCanonicalPath(common, auth.CommonDir) {
			return "", "", fmt.Errorf("%w: gitdir=%s commondir=%s", ErrUnauthorizedGitDir, gitDir, common)
		}
		return gitDir, common, nil
	}
	return ResolveGitDirs(dir, txn.cfg.AuthorizedGitDirs...)
}

func (txn *Transaction) recordRootGitDirIdentity(rootID, gitDir, common string) error {
	ident, err := captureGitDirIdentity(gitDir, common)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnauthorizedGitDir, err)
	}
	if txn.journal.GitDirs == nil {
		txn.journal.GitDirs = map[string]gitDirIdentity{}
	}
	if existing, ok := txn.journal.GitDirs[rootID]; ok && !sameGitDirIdentity(ident, existing) {
		return fmt.Errorf("%w: git metadata changed for root %q", ErrUnauthorizedGitDir, rootID)
	}
	txn.journal.GitDirs[rootID] = ident
	return nil
}

func (txn *Transaction) verifyRecordedRootGitDirs(rootID, dir string) (string, string, error) {
	gitDir, common, err := txn.resolveRootGitDirs(rootID, dir)
	if err != nil {
		return "", "", err
	}
	if recorded, ok := txn.journal.GitDirs[rootID]; ok {
		live, err := captureGitDirIdentity(gitDir, common)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrUnauthorizedGitDir, err)
		}
		if !sameGitDirIdentity(live, recorded) {
			return "", "", fmt.Errorf("%w: git metadata changed for root %q", ErrUnauthorizedGitDir, rootID)
		}
	}
	return gitDir, common, nil
}

func captureGitDirIdentity(gitDir, common string) (gitDirIdentity, error) {
	canonGit, err := canonicalDirPath(gitDir)
	if err != nil {
		return gitDirIdentity{}, err
	}
	canonCommon, err := canonicalDirPath(common)
	if err != nil {
		return gitDirIdentity{}, err
	}
	gitIdent, err := fspath.EntryIdentity(canonGit)
	if err != nil {
		return gitDirIdentity{}, err
	}
	commonIdent, err := fspath.EntryIdentity(canonCommon)
	if err != nil {
		return gitDirIdentity{}, err
	}
	return gitDirIdentity{
		GitDir:         canonGit,
		GitIdentity:    gitIdent,
		CommonDir:      canonCommon,
		CommonIdentity: commonIdent,
	}, nil
}

func sameGitDirIdentity(live, recorded gitDirIdentity) bool {
	return sameCanonicalPath(live.GitDir, recorded.GitDir) &&
		fspath.SameEntryAcrossRestart(recorded.GitIdentity, live.GitIdentity) &&
		sameCanonicalPath(live.CommonDir, recorded.CommonDir) &&
		fspath.SameEntryAcrossRestart(recorded.CommonIdentity, live.CommonIdentity)
}

func sameCanonicalPath(a, b string) bool {
	ca, errA := canonicalDirPath(a)
	cb, errB := canonicalDirPath(b)
	return errA == nil && errB == nil && ca == cb
}

func canonicalDirPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", ErrUnauthorizedGitDir
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(realPath), nil
}
