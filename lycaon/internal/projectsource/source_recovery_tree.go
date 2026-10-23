package projectsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// A retained tree keeps its original entries under the recovery owner's root.
// Version identifies its durable binding independently of blob manifests.
type sourceRecoveryTree struct {
	AllocatedBytes int64  `json:"allocated_bytes"`
	Version        int    `json:"version"`
	Identity       string `json:"identity"`
}

func (t *sourceRecoveryTree) UnmarshalJSON(raw []byte) error {
	type binding sourceRecoveryTree
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"version", "identity", "allocated_bytes"} {
		if _, ok := fields[key]; !ok {
			return ErrSourceRecoveryFailed
		}
	}
	var decoded binding
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Version != 1 || decoded.Identity == "" || decoded.AllocatedBytes < 0 {
		return ErrSourceRecoveryFailed
	}
	*t = sourceRecoveryTree(decoded)
	return nil
}

func (s *sourceRecovery) treeLocation(id string) (fseffect.Location, error) {
	if id == "" {
		return fseffect.Location{}, ErrSourceRecoveryFailed
	}
	var root string
	if s.objects != nil {
		root = s.objects.RecoveryTreeRoot()
	} else {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		if s.treeRoot == "" {
			var err error
			s.treeRoot, err = os.MkdirTemp("", "painted-wolf-recovery-")
			if err != nil {
				return fseffect.Location{}, err
			}
		}
		root = s.treeRoot
	}
	return fseffect.Location{Root: root, Rel: sourceledger.RecoveryTreeName(id)}, nil
}

func (s *sourceRecovery) canRetainTree(location fseffect.Location, sameFilesystem func(string, string) (bool, error)) (bool, error) {
	// The reservation's name is not an entry publication. Its private parent
	// is created before review so the filesystem fact is part of admission.
	target, err := s.treeLocation("admission")
	if err != nil {
		return false, err
	}
	if err := fseffect.MkdirAll(fseffect.Location{Root: filepath.Dir(filepath.Dir(target.Root)), Rel: filepath.Join(filepath.Base(filepath.Dir(target.Root)), filepath.Base(target.Root))}, 0o700); err != nil {
		return false, err
	}
	return sameFilesystem(filepath.Join(location.Root, location.Rel), target.Root)
}

func (s *SourceEffects) retainSourceTree(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	binding := plan.RetainedTree
	if binding == nil || binding.Version != 1 || binding.Identity == "" || plan.EntryRecoveryKey != plan.RecoveryID {
		return ErrSourceRecoveryFailed
	}
	target, err := s.recovery.treeLocation(plan.RecoveryID)
	if err != nil {
		return err
	}
	held := filepath.Join(target.Root, target.Rel)
	if !sourceMutationPathExists(plan.AbsPath) {
		if !plan.DeleteStarted {
			return ErrSourceMutationDiverged
		}
		return requireSourceIdentity(held, binding.Identity)
	}
	if sourceMutationPathExists(held) {
		return ErrSourceMutationDiverged
	}
	if err := requireSourceIdentity(plan.AbsPath, binding.Identity); err != nil {
		return err
	}
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	plan.DeleteStarted, plan.DeleteIdentity = true, binding.Identity
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	sha, size, err := sourceTreeFingerprint(ctx, plan.AbsPath)
	if err != nil {
		return err
	}
	if sha != plan.TreeSHA || !size.AllocationKnown || size.AllocatedBytes != binding.AllocatedBytes {
		return ErrSourceMutationDiverged
	}
	err = s.relocateRecoveryTree(ctx, row.ID, fseffect.Location{Root: plan.RootPath, Rel: plan.Path}, target, binding.Identity)
	var unavailable *sourceeffect.RecoveryUnavailableError
	if errors.As(err, &unavailable) {
		// Keep attempt facts, but do not retain an unused recovery claim.
		plan.RetainedTree, plan.EntryRecoveryKey, plan.RecoveryID, plan.RecoveryCount = nil, "", "", 0
	}
	return err
}

// Classification follows the exact entries after the guarded rename attempt.
// A flush failure cannot be treated as an unperformed deletion or restoration.
func (s *SourceEffects) relocateRecoveryTree(ctx context.Context, operationID string, from, to fseffect.Location, identity string) error {
	moveErr := s.relocate(from, to, identity)
	if moveErr == nil {
		return nil
	}
	current, currentErr := fspath.EntryIdentity(filepath.Join(from.Root, from.Rel))
	published, publishedErr := fspath.EntryIdentity(filepath.Join(to.Root, to.Rel))
	if publishedErr == nil && published == identity && (os.IsNotExist(currentErr) || (currentErr == nil && current != identity)) {
		return &sourceeffect.AppliedError{OperationID: operationID, Cause: moveErr}
	}
	if currentErr == nil && current == identity && os.IsNotExist(publishedErr) && !errors.Is(moveErr, fseffect.ErrPostcondition) && !os.IsExist(moveErr) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &sourceeffect.RecoveryUnavailableError{Cause: moveErr}
	}
	return errors.Join(ErrSourceMutationDiverged, moveErr, currentErr, publishedErr)
}

func (s *SourceEffects) restoreSourceTree(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	binding := plan.RetainedTree
	if binding == nil || binding.Version != 1 || binding.Identity == "" || plan.EntryRecoveryKey != plan.RecoveryID {
		return ErrSourceRecoveryFailed
	}
	source, err := s.recovery.treeLocation(plan.RecoveryID)
	if err != nil {
		return err
	}
	if sourceMutationPathExists(plan.AbsPath) {
		if !plan.EffectStarted {
			return ErrSourceExists
		}
		return requireSourceIdentity(plan.AbsPath, binding.Identity)
	}
	held := filepath.Join(source.Root, source.Rel)
	if err := requireSourceIdentity(held, binding.Identity); err != nil {
		return err
	}
	if err := requireSourceFingerprint(ctx, held, plan.TreeSHA); err != nil {
		return err
	}
	plan.DestinationIdentity = binding.Identity
	if err := s.startSourceEffect(ctx, row); err != nil {
		return err
	}
	if err := requireSourceFingerprint(ctx, held, plan.TreeSHA); err != nil {
		return err
	}
	return s.relocateRecoveryTree(ctx, row.ID, source, fseffect.Location{Root: plan.RootPath, Rel: plan.Path}, binding.Identity)
}

// Close releases only ephemeral memory-mode recovery. Durable history belongs
// to the retained store and remains available after the service closes.
func (s *SourceMutationService) Close() error {
	if s == nil || s.recovery.objects != nil {
		return nil
	}
	s.recovery.lifecycle.Lock()
	defer s.recovery.lifecycle.Unlock()
	s.recovery.stateMu.Lock()
	defer s.recovery.stateMu.Unlock()
	if s.recovery.treeRoot == "" {
		return nil
	}
	root := s.recovery.treeRoot
	identity, err := fspath.EntryIdentity(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := fseffect.RemoveTreeGuarded(fseffect.Location{Root: filepath.Dir(root), Rel: filepath.Base(root)}, identity); err != nil {
		return fmt.Errorf("release memory recovery: %w", err)
	}
	s.recovery.treeRoot = ""
	return nil
}
