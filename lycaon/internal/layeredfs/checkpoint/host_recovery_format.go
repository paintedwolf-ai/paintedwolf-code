package checkpoint

import (
	"encoding/hex"
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
	switch image.Kind {
	case layeredfs.EntryRegular:
		digest, err := hex.DecodeString(image.SHA256)
		return err == nil && len(digest) == 32 && image.LinkTarget == ""
	case layeredfs.EntryDirectory:
		return image.SHA256 == "" && image.LinkTarget == "" && image.Size == 0
	case layeredfs.EntrySymlink:
		return image.SHA256 == "" && image.Mode == 0 && image.Size == 0 && len(image.Xattrs) == 0
	case layeredfs.EntryAbsent:
		return image.SHA256 == "" && image.LinkTarget == "" && image.Mode == 0 && image.Size == 0 && len(image.Xattrs) == 0
	default:
		return false
	}
}
