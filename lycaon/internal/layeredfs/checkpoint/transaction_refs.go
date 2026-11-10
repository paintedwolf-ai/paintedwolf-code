package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// prepareRefs records each root's ref undo. Only the built-in namespaced importer runs, because its
// refs can be restored by compare-and-swap.
func (txn *Transaction) prepareRefs(m Manifest, targetRoots map[string]string) ([]refUndo, error) {
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
	if primaryID := selectPrimaryRootID(targetRoots); len(m.PrivateGit.Refs) > 0 && repositoryPresent(targetRoots[primaryID]) {
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
	gitDir, common, err := ResolveGitDirs(dir, txn.cfg.AuthorizedGitDirs...)
	if err != nil {
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
	gitDir, common, err := ResolveGitDirs(primary, txn.cfg.AuthorizedGitDirs...)
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
