package checkpoint

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

type rootIdentity struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
}

func (txn *Transaction) rootIdentities() (map[string]rootIdentity, error) {
	return identifyRoots(txn.targets)
}

func identifyRoots(targets *promotionTargets) (map[string]rootIdentity, error) {
	roots := make(map[string]rootIdentity, len(targets.roots))
	byPath := make(map[string]rootIdentity, len(targets.roots))
	for id, path := range targets.roots {
		if cached, ok := byPath[path]; ok {
			roots[id] = cached
			continue
		}
		root, err := targets.openTargetRoot(id)
		if err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		identity, err := fspath.EntryIdentity(absolute)
		held, statErr := root.Stat(".")
		current, pathErr := os.Lstat(absolute)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		if statErr != nil {
			return nil, statErr
		}
		if pathErr != nil {
			return nil, pathErr
		}
		if !os.SameFile(held, current) {
			return nil, ErrStalePreviewTargetChanged
		}
		ident := rootIdentity{Path: absolute, Identity: identity}
		byPath[path] = ident
		roots[id] = ident
	}
	return roots, nil
}

// sameRoot tolerates a device number that a restart moved.
func sameRoot(live, recorded rootIdentity) bool {
	return live.Path == recorded.Path && fspath.SameEntryAcrossRestart(recorded.Identity, live.Identity)
}

func safeComponent(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.IsLocal(id) && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

// validateJournal refuses any record no build writes, before recovery touches a target.
func (txn *Transaction) validateJournal(tx transactionJournal) error {
	if tx.Version != transactionVersion || !safeComponent(tx.ID) {
		return ErrInvalidManifest
	}
	if tx.Phase != transactionPhasePrepared && tx.Phase != transactionPhaseApplying {
		return ErrInvalidManifest
	}
	recorded := make(map[string]string, len(tx.Roots))
	for id, root := range tx.Roots {
		recorded[id] = root.Path
	}
	roots, err := identifyRoots(&promotionTargets{roots: recorded})
	if err != nil {
		return err
	}
	if !maps.EqualFunc(roots, tx.Roots, sameRoot) {
		return fmt.Errorf("checkpoint: recovery target roots changed")
	}
	for _, record := range tx.Records {
		if !filepath.IsAbs(record) {
			return ErrInvalidManifest
		}
	}
	seen := map[layeredfs.QualifiedPath]bool{}
	byPath := make(map[layeredfs.QualifiedPath]targetUndo, len(tx.Targets))
	for _, target := range tx.Targets {
		if !filepath.IsLocal(target.Path.Rel) || target.Path.Rel == "." || seen[target.Path] {
			return ErrInvalidManifest
		}
		if _, known := tx.Roots[target.Path.RootID]; !known {
			return ErrInvalidManifest
		}
		seen[target.Path] = true
		if !validImage(target.Before) || !validImage(target.After) {
			return ErrInvalidManifest
		}
		if target.After.HardLinkSource != nil || target.After.HardLinkGroup != "" || target.After.RetainedSlot != "" {
			return ErrInvalidManifest
		}
		for _, prior := range target.Prior {
			if !validImage(prior) {
				return ErrInvalidManifest
			}
		}
		byPath[target.Path] = target
	}
	for _, target := range tx.Targets {
		source := target.Before.HardLinkSource
		if source == nil {
			continue
		}
		original, known := byPath[*source]
		if !known || *source == target.Path || source.RootID != target.Path.RootID || original.Before.HardLinkSource != nil || !sameTarget(original.Before, target.Before) {
			return ErrInvalidManifest
		}
	}
	return validateJournalRefs(tx)
}

// validateJournalRefs binds recorded ref undo to the checkpoint that names those refs.
func validateJournalRefs(tx transactionJournal) error {
	if tx.Checkpoint == nil {
		if len(tx.Refs) != 0 {
			return ErrInvalidManifest
		}
		return nil
	}
	if !safeComponent(tx.Checkpoint.ID) || !safeComponent(tx.Checkpoint.WorkerID) || !filepath.IsAbs(tx.Checkpoint.StoreDir) {
		return ErrInvalidManifest
	}
	store, err := NewStore(tx.Checkpoint.StoreDir)
	if err != nil {
		return err
	}
	m, err := store.LoadManifest(tx.Checkpoint.ID)
	if err != nil {
		return err
	}
	if tx.Checkpoint.WorkerID != m.WorkerID {
		return ErrOwnershipMismatch
	}
	if len(tx.Refs) == 0 {
		return nil
	}
	allowed := map[string]string{}
	addAllowed := func(rootID string, named map[string]string) error {
		for name, oid := range named {
			suffix, err := ValidateGitRefName(name)
			if err != nil {
				return err
			}
			clean := strings.ToLower(strings.TrimSpace(oid))
			allowed[rootID+"\x00refs/lycaon/workers/"+m.WorkerID+"/"+suffix] = clean
			allowed[rootID+"\x00refs/lycaon/checkpoints/"+m.CheckpointID+"/"+suffix] = clean
		}
		return nil
	}
	if err := addAllowed(tx.Checkpoint.PrimaryRootID, m.PrivateGit.Refs); err != nil {
		return err
	}
	for rootID, root := range m.PrivateGit.Roots {
		if err := addAllowed(rootID, root.Refs); err != nil {
			return err
		}
	}
	for _, ref := range tx.Refs {
		key := ref.RootID + "\x00" + ref.Name
		if _, known := tx.Roots[ref.RootID]; !known || allowed[key] != ref.After || !isHexOID(ref.After) || (ref.Before != "" && ref.Before != ref.After) {
			return ErrInvalidManifest
		}
	}
	return nil
}

func validImage(image targetImage) bool {
	if image.Size < 0 || image.Mode != image.Mode.Perm() {
		return false
	}
	if image.RetainedSlot != "" && !safeComponent(image.RetainedSlot) {
		return false
	}
	if strings.ContainsAny(image.HardLinkGroup, "/\\\x00") {
		return false
	}
	switch image.Kind {
	case layeredfs.EntryRegular:
		digest, err := hex.DecodeString(image.SHA256)
		return err == nil && len(digest) == 32 && image.LinkTarget == ""
	case layeredfs.EntryDirectory:
		return image.HardLinkSource == nil && image.HardLinkGroup == "" && image.RetainedSlot == "" && image.SHA256 == "" && image.LinkTarget == "" && image.Size == 0
	case layeredfs.EntrySymlink:
		return image.HardLinkSource == nil && image.HardLinkGroup == "" && image.RetainedSlot == "" && image.SHA256 == "" && image.Mode == 0 && image.Size == 0 && len(image.Xattrs) == 0
	case layeredfs.EntryAbsent:
		return image.HardLinkSource == nil && image.HardLinkGroup == "" && image.RetainedSlot == "" && image.SHA256 == "" && image.LinkTarget == "" && image.Mode == 0 && image.Size == 0 && len(image.Xattrs) == 0
	default:
		return false
	}
}

func sameTarget(a, b targetImage) bool {
	if a.Kind == "" {
		a.Kind = layeredfs.EntryAbsent
	}
	if b.Kind == "" {
		b.Kind = layeredfs.EntryAbsent
	}
	return a.Kind == b.Kind && a.Mode == b.Mode && a.SHA256 == b.SHA256 && a.Size == b.Size && a.LinkTarget == b.LinkTarget && maps.EqualFunc(a.Xattrs, b.Xattrs, bytes.Equal)
}

func (txn *Transaction) validateTargets(tx transactionJournal) error {
	targets := tx.Targets
	known := map[layeredfs.QualifiedPath]targetImage{}
	expectedSlots := map[string]bool{}
	for _, target := range targets {
		if !filepath.IsLocal(target.Path.Rel) || target.Path.Rel == "." {
			return ErrInvalidManifest
		}
		if err := txn.captureTree(target.Path, known); err != nil {
			return err
		}
		if err := txn.validateRetainedSlot(target.Path, target.Before); err != nil {
			return err
		}
		if target.Before.RetainedSlot != "" {
			expectedSlots[target.Before.RetainedSlot] = true
		}
	}
	for _, pending := range tx.PendingSlots {
		expectedSlots[pending] = true
	}
	if err := validateRetainedDirEntries(txn.retainedDir(), expectedSlots); err != nil {
		return err
	}
	for _, target := range targets {
		current := known[target.Path]
		if !target.holds(current) {
			if tx.RollingBack && target.Before.HardLinkSource != nil && current.Kind == layeredfs.EntryAbsent {
				delete(known, target.Path)
				continue
			}
			return fmt.Errorf("checkpoint: cannot recover diverged target %s", pathKey(target.Path))
		}
		delete(known, target.Path)
	}
	if len(known) != 0 {
		return fmt.Errorf("checkpoint: cannot recover directory with unrecorded children")
	}
	return nil
}

func validateRetainedDirEntries(dir string, expected map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !expected[entry.Name()] {
			return ErrInvalidManifest
		}
	}
	return nil
}
