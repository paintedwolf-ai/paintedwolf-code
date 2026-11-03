package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (host *HostPromotion) captureHostTarget(path layeredfs.QualifiedPath) (hostTargetImage, error) {
	obs, err := host.targets.observeTarget(path)
	if err != nil {
		return hostTargetImage{}, err
	}
	image := hostTargetImage{Kind: obs.Kind, Mode: obs.Mode.Perm(), SHA256: obs.SHA256, Size: obs.Size, LinkTarget: obs.LinkTarget}
	if obs.Kind == layeredfs.EntryAbsent || obs.Kind == layeredfs.EntrySymlink {
		image.Mode = 0
		image.Size = 0
		return image, nil
	}
	root, err := host.targets.openTargetRoot(path.RootID)
	if err != nil {
		return image, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(path.Rel)
	if err != nil {
		return image, err
	}
	defer func() { _ = file.Close() }()
	image.Xattrs, err = readFileXattrs(file)
	if err != nil {
		return image, err
	}
	if obs.Kind == layeredfs.EntryRegular {
		body, err := io.ReadAll(file)
		if err != nil {
			return image, err
		}
		if sha256Hex(body) != obs.SHA256 {
			return image, ErrStalePreviewTargetChanged
		}
		_, _, err = host.store.WriteBlobBytes(body)
		if err != nil {
			return image, err
		}
	} else {
		image.Size = 0
	}
	return image, nil
}

func (host *HostPromotion) captureHostTree(path layeredfs.QualifiedPath, images map[layeredfs.QualifiedPath]hostTargetImage) error {
	if _, found := images[path]; found {
		return nil
	}
	image, err := host.captureHostTarget(path)
	if err != nil {
		return err
	}
	images[path] = image
	if image.Kind != layeredfs.EntryDirectory {
		return nil
	}
	root, err := host.targets.openTargetRoot(path.RootID)
	if err != nil {
		return err
	}
	entries, err := root.Open(path.Rel)
	if err != nil {
		_ = root.Close()
		return err
	}
	children, err := entries.ReadDir(-1)
	_ = entries.Close()
	_ = root.Close()
	if err != nil {
		return err
	}
	for _, child := range children {
		next := layeredfs.QualifiedPath{RootID: path.RootID, Rel: filepath.Join(path.Rel, child.Name())}
		if err := host.captureHostTree(next, images); err != nil {
			return err
		}
	}
	return nil
}

func (host *HostPromotion) prepareHostTargets(preview *PromotionPreview) ([]hostTargetUndo, error) {
	before := map[layeredfs.QualifiedPath]hostTargetImage{}
	for _, change := range preview.PlannedChanges {
		if err := host.captureHostTree(change.Path, before); err != nil {
			return nil, err
		}
		// Record parents created implicitly by MkdirAll, without capturing unrelated trees.
		for parent := filepath.Dir(change.Path.Rel); parent != "."; parent = filepath.Dir(parent) {
			path := layeredfs.QualifiedPath{RootID: change.Path.RootID, Rel: parent}
			obs, err := host.targets.observeTarget(path)
			if err != nil {
				return nil, err
			}
			if obs.Kind != layeredfs.EntryAbsent {
				break
			}
			before[path] = hostTargetImage{Kind: layeredfs.EntryAbsent}
		}
	}
	after := maps.Clone(before)
	for _, change := range preview.PlannedChanges {
		if err := host.planHostTarget(after, change); err != nil {
			return nil, err
		}
	}
	paths := make([]layeredfs.QualifiedPath, 0, len(after))
	for path := range after {
		paths = append(paths, path)
	}
	slices.SortFunc(paths, func(a, b layeredfs.QualifiedPath) int { return strings.Compare(pathKey(a), pathKey(b)) })
	out := make([]hostTargetUndo, 0, len(paths))
	for _, path := range paths {
		out = append(out, hostTargetUndo{Path: path, Before: before[path], After: after[path]})
	}
	return out, nil
}

func (host *HostPromotion) planHostTarget(images map[layeredfs.QualifiedPath]hostTargetImage, ch PlannedChange) error {
	if ch.Action == ActionNoop {
		return nil
	}
	if ch.Action == ActionDelete || ch.Action == ActionReplaceWithDirectory || ch.Action == ActionReplaceDirWithRegular || ch.Action == ActionSymlink || ch.Action == ActionHardLink {
		for path := range images {
			if path.RootID == ch.Path.RootID && (path.Rel == ch.Path.Rel || strings.HasPrefix(path.Rel, ch.Path.Rel+string(filepath.Separator))) {
				images[path] = hostTargetImage{Kind: layeredfs.EntryAbsent}
			}
		}
	}
	next := hostTargetImage{}
	switch ch.Action {
	case ActionDelete:
		next.Kind = layeredfs.EntryAbsent
	case ActionMkdir, ActionReplaceWithDirectory:
		next = images[ch.Path]
		next.Kind = layeredfs.EntryDirectory
		next.Size = 0
		next.Mode = pickMode(ch.Mode, 0755).Perm()
		next.Xattrs = maps.Clone(next.Xattrs)
		if next.Xattrs == nil {
			next.Xattrs = map[string][]byte{}
		}
		maps.Copy(next.Xattrs, ch.Xattrs)
	case ActionWriteRegular, ActionReplaceDirWithRegular:
		body := ch.MergedBytes
		if body == nil && ch.MergedSHA256 != "" {
			var err error
			body, err = host.store.ReadBlob(ch.MergedSHA256)
			if err != nil {
				return err
			}
		}
		next = hostTargetImage{Kind: layeredfs.EntryRegular, Mode: pickMode(ch.Mode, 0644).Perm(), SHA256: sha256Hex(body), Size: int64(len(body)), Xattrs: cloneXattrs(ch.Xattrs)}
	case ActionSymlink:
		next = hostTargetImage{Kind: layeredfs.EntrySymlink, LinkTarget: ch.LinkTarget}
	case ActionHardLink:
		if ch.HardLinkSource == nil || ch.HardLinkSource.RootID != ch.Path.RootID {
			return fmt.Errorf("checkpoint: invalid hard-link source")
		}
		var found bool
		next, found = images[*ch.HardLinkSource]
		if !found {
			var err error
			next, err = host.captureHostTarget(*ch.HardLinkSource)
			if err != nil {
				return err
			}
		}
		if next.Kind != layeredfs.EntryRegular {
			return fmt.Errorf("checkpoint: hard-link source is not regular")
		}
	default:
		return fmt.Errorf("checkpoint: unsupported host action %q", ch.Action)
	}
	images[ch.Path] = next
	if ch.Action != ActionDelete {
		for parent := filepath.Dir(ch.Path.Rel); parent != "."; parent = filepath.Dir(parent) {
			path := layeredfs.QualifiedPath{RootID: ch.Path.RootID, Rel: parent}
			if old, found := images[path]; found && old.Kind == layeredfs.EntryAbsent {
				images[path] = hostTargetImage{Kind: layeredfs.EntryDirectory, Mode: 0755}
			}
		}
	}
	return nil
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

func (host *HostPromotion) validateHostTargets(targets []hostTargetUndo) error {
	known := map[layeredfs.QualifiedPath]hostTargetImage{}
	for _, target := range targets {
		if !filepath.IsLocal(target.Path.Rel) || target.Path.Rel == "." {
			return ErrInvalidManifest
		}
		if err := host.captureHostTree(target.Path, known); err != nil {
			return err
		}
	}
	for _, target := range targets {
		current := known[target.Path]
		if !sameHostTarget(current, target.Before) && !sameHostTarget(current, target.After) {
			return fmt.Errorf("checkpoint: cannot recover diverged target %s", pathKey(target.Path))
		}
		delete(known, target.Path)
	}
	if len(known) != 0 {
		return fmt.Errorf("checkpoint: cannot recover directory with unrecorded children")
	}
	return nil
}

func (host *HostPromotion) restoreHostTargets(targets []hostTargetUndo) error {
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, func(a, b hostTargetUndo) int { return len(b.Path.Rel) - len(a.Path.Rel) })
	// Remove only recorded entries, never an unobserved child added concurrently.
	for _, target := range ordered {
		current, err := host.captureHostTarget(target.Path)
		if err != nil {
			return err
		}
		if sameHostTarget(current, target.Before) {
			continue
		}
		if !sameHostTarget(current, target.After) {
			return fmt.Errorf("checkpoint: target changed during recovery: %s", pathKey(target.Path))
		}
		if current.Kind != layeredfs.EntryAbsent && (target.Before.Kind == layeredfs.EntryAbsent || target.Before.Kind == "" || current.Kind != target.Before.Kind) {
			root, err := host.targets.openTargetRoot(target.Path.RootID)
			if err != nil {
				return err
			}
			err = root.Remove(target.Path.Rel)
			_ = root.Close()
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	slices.Reverse(ordered)
	for _, target := range ordered {
		before := target.Before
		if before.Kind == layeredfs.EntryAbsent || before.Kind == "" {
			continue
		}
		change := PlannedChange{Path: target.Path, Mode: before.Mode, Xattrs: before.Xattrs}
		switch before.Kind {
		case layeredfs.EntryRegular:
			change.Action = ActionWriteRegular
			change.MergedSHA256 = before.SHA256
		case layeredfs.EntryDirectory:
			change.Action = ActionMkdir
		case layeredfs.EntrySymlink:
			change.Action = ActionSymlink
			change.LinkTarget = before.LinkTarget
		default:
			return ErrInvalidManifest
		}
		current, err := host.captureHostTarget(target.Path)
		if err != nil {
			return err
		}
		if sameHostTarget(current, before) {
			continue
		}
		if err := host.targets.applyPlannedChange(change); err != nil {
			return err
		}
		if before.Kind == layeredfs.EntryRegular || before.Kind == layeredfs.EntryDirectory {
			root, err := host.targets.openTargetRoot(target.Path.RootID)
			if err != nil {
				return err
			}
			file, err := root.Open(target.Path.Rel)
			if err != nil {
				_ = root.Close()
				return err
			}
			err = restoreFileXattrs(file, before.Xattrs)
			if err == nil {
				err = syncFile(file)
			}
			_ = file.Close()
			_ = root.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
