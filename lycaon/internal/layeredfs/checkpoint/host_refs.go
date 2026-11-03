package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

func (host *HostPromotion) prepareHostRefs(_ context.Context, m Manifest) ([]hostRefUndo, error) {
	if len(m.PrivateGit.Refs) == 0 {
		return nil, nil
	}
	if err := ValidateGitRefSegment(m.WorkerID, "worker"); err != nil {
		return nil, err
	}
	if err := ValidateGitRefSegment(m.CheckpointID, "checkpoint"); err != nil {
		return nil, err
	}
	primary := selectPrimaryRootDir(host.targets.roots)
	gitDir, common, err := ResolveGitDirs(primary, host.authorizedGitDirs...)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m.PrivateGit.Refs))
	for name := range m.PrivateGit.Refs {
		names = append(names, name)
	}
	slices.Sort(names)
	var refs []hostRefUndo
	for _, name := range names {
		suffix, err := ValidateGitRefName(name)
		if err != nil {
			return nil, err
		}
		oid := strings.ToLower(strings.TrimSpace(m.PrivateGit.Refs[name]))
		if !isHexOID(oid) || isAllZeroOID(oid) {
			return nil, fmt.Errorf("checkpoint: invalid ref object")
		}
		for _, ref := range []string{"refs/lycaon/workers/" + m.WorkerID + "/" + suffix, "refs/lycaon/checkpoints/" + m.CheckpointID + "/" + suffix} {
			old := resolveExistingGitRef(common, gitDir, ref)
			if old != "" && old != oid {
				return nil, ErrCheckpointRefConflict
			}
			refs = append(refs, hostRefUndo{Name: ref, Before: old, After: oid})
		}
	}
	return refs, nil
}

func (host *HostPromotion) rollbackHostRefs(ctx context.Context, refs []hostRefUndo) error {
	if len(refs) == 0 {
		return nil
	}
	primary := selectPrimaryRootDir(host.targets.roots)
	gitDir, common, err := ResolveGitDirs(primary, host.authorizedGitDirs...)
	if err != nil {
		return err
	}
	var input strings.Builder
	for _, ref := range refs {
		if _, err := ValidateGitRefName(ref.Name); err != nil {
			return err
		}
		if !strings.HasPrefix(ref.Name, "refs/lycaon/") || !isHexOID(ref.After) || (ref.Before != "" && !isHexOID(ref.Before)) {
			return ErrInvalidManifest
		}
		current := resolveExistingGitRef(common, gitDir, ref.Name)
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
