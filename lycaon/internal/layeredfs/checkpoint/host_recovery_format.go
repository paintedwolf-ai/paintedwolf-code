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

type hostRootIdentity struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
}

func (host *HostPromotion) hostRootIdentities() (map[string]hostRootIdentity, error) {
	roots := make(map[string]hostRootIdentity, len(host.targets.roots))
	for id, path := range host.targets.roots {
		root, err := host.targets.openTargetRoot(id)
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
		roots[id] = hostRootIdentity{Path: absolute, Identity: identity}
	}
	return roots, nil
}

func hostComponent(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.IsLocal(id) && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

func (host *HostPromotion) validateHostTransaction(tx hostTransaction) error {
	if tx.Version != 1 || !hostComponent(tx.CheckpointID) || !hostComponent(tx.WorkerID) {
		return ErrInvalidManifest
	}
	roots, err := host.hostRootIdentities()
	if err != nil {
		return err
	}
	if !maps.Equal(roots, tx.Roots) {
		return fmt.Errorf("checkpoint: recovery target roots changed")
	}
	m, err := host.store.LoadManifest(tx.CheckpointID)
	if err != nil {
		return err
	}
	if tx.WorkerID != m.WorkerID {
		return ErrOwnershipMismatch
	}
	seen := map[layeredfs.QualifiedPath]bool{}
	byPath := make(map[layeredfs.QualifiedPath]hostTargetUndo, len(tx.Targets))
	for _, target := range tx.Targets {
		if !filepath.IsLocal(target.Path.Rel) || target.Path.Rel == "." || seen[target.Path] {
			return ErrInvalidManifest
		}
		if _, known := tx.Roots[target.Path.RootID]; !known {
			return ErrInvalidManifest
		}
		seen[target.Path] = true
		if !validHostImage(target.Before) || !validHostImage(target.After) {
			return ErrInvalidManifest
		}
		if target.After.HardLinkSource != nil || target.After.HardLinkGroup != "" || target.After.RetainedSlot != "" {
			return ErrInvalidManifest
		}
		byPath[target.Path] = target
	}
	for _, target := range tx.Targets {
		source := target.Before.HardLinkSource
		if source == nil {
			continue
		}
		original, known := byPath[*source]
		if !known || *source == target.Path || source.RootID != target.Path.RootID || original.Before.HardLinkSource != nil || !sameHostTarget(original.Before, target.Before) {
			return ErrInvalidManifest
		}
	}
	allowed := map[string]string{}
	for name, oid := range m.PrivateGit.Refs {
		suffix, err := ValidateGitRefName(name)
		if err != nil {
			return err
		}
		allowed["refs/lycaon/workers/"+m.WorkerID+"/"+suffix] = strings.ToLower(strings.TrimSpace(oid))
		allowed["refs/lycaon/checkpoints/"+m.CheckpointID+"/"+suffix] = strings.ToLower(strings.TrimSpace(oid))
	}
	for _, ref := range tx.Refs {
		if allowed[ref.Name] != ref.After || !isHexOID(ref.After) || (ref.Before != "" && ref.Before != ref.After) {
			return ErrInvalidManifest
		}
		delete(allowed, ref.Name)
	}
	if len(allowed) != 0 {
		return ErrInvalidManifest
	}
	return nil
}

func validHostImage(image hostTargetImage) bool {
	if image.Size < 0 || image.Mode != image.Mode.Perm() {
		return false
	}
	if image.RetainedSlot != "" && !hostComponent(image.RetainedSlot) {
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

func sameHostTarget(a, b hostTargetImage) bool {
	if a.Kind == "" {
		a.Kind = layeredfs.EntryAbsent
	}
	if b.Kind == "" {
		b.Kind = layeredfs.EntryAbsent
	}
	return a.Kind == b.Kind && a.Mode == b.Mode && a.SHA256 == b.SHA256 && a.Size == b.Size && a.LinkTarget == b.LinkTarget && maps.EqualFunc(a.Xattrs, b.Xattrs, bytes.Equal)
}

func (host *HostPromotion) validateHostTargets(tx hostTransaction) error {
	targets := tx.Targets
	known := map[layeredfs.QualifiedPath]hostTargetImage{}
	expectedSlots := map[string]bool{}
	for _, target := range targets {
		if !filepath.IsLocal(target.Path.Rel) || target.Path.Rel == "." {
			return ErrInvalidManifest
		}
		if err := host.captureHostTree(target.Path, known); err != nil {
			return err
		}
		if err := host.validateRetainedSlot(target.Path, target.Before); err != nil {
			return err
		}
		if target.Before.RetainedSlot != "" {
			expectedSlots[target.Before.RetainedSlot] = true
		}
	}
	if err := validateRetainedDirEntries(host.retainedDir(), expectedSlots); err != nil {
		return err
	}
	for _, target := range targets {
		current := known[target.Path]
		if !sameHostTarget(current, target.Before) && !sameHostTarget(current, target.After) {
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
