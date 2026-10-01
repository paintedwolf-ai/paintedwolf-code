package extpacks

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// DiscoverLockedContent loads package bodies named by the lock graph.
func DiscoverLockedContent(projectDirs []string) ([]PackContent, error) {
	release, err := AcquireIntentLocks(projectDirs)
	if err != nil {
		return nil, err
	}
	defer release()
	return discoverLockedContent(projectDirs)
}

func discoverLockedContent(projectDirs []string) ([]PackContent, error) {
	lock, err := LoadDeviceLock()
	if err != nil {
		return nil, err
	}
	desired, _, err := LoadMergedDesired(projectDirs)
	if err != nil {
		return nil, err
	}
	if err := validateDesiredLock(desired, lock); err != nil {
		return nil, err
	}
	lock, err = reachableLock(lock, desired)
	if err != nil {
		return nil, err
	}
	out := make([]PackContent, 0, len(lock.Packages))
	for _, locked := range lock.Packages {
		out = append(out, admitLockedPackage(locked))
	}
	return out, nil
}

type packInspection struct {
	content   PackContent
	integrity string
	root      string
}

func admitLockedPackage(locked LockedPackage) PackContent {
	ins, err := inspectLockedPackage(locked)
	if err != nil {
		return isolateLockedFault(locked, ins.root, err)
	}
	if ins.integrity == locked.Integrity {
		return ins.content
	}
	if locked.Kind == PackKindPath {
		ins.content.NeedsReload = true
		return ins.content
	}
	return stubLockedPack(locked, ins.root, BlockedIntegrity,
		fmt.Sprintf("integrity mismatch: lock has %s, content has %s", locked.Integrity, ins.integrity))
}

func isolateLockedFault(locked LockedPackage, root string, err error) PackContent {
	if locked.Kind == PackKindPath {
		if isPathUnavailable(err) {
			return pathMissingContent(locked, root)
		}
		return stubLockedPack(locked, root, BlockedInvalid, err.Error())
	}
	return stubLockedPack(locked, root, BlockedIntegrity, err.Error())
}

func pathMissingContent(locked LockedPackage, root string) PackContent {
	lockedCopy := locked
	return PackContent{
		Pack:     Pack{ID: locked.ID, Root: OnDisk(root)},
		Manifest: Manifest{ID: locked.ID, Name: locked.ID, Version: locked.Version},
		Kind:     PackKindPath,
		Locked:   &lockedCopy,
	}
}

func stubLockedPack(locked LockedPackage, root string, reason BlockedReason, detail string) PackContent {
	lockedCopy := locked
	return PackContent{
		Pack:       Pack{ID: locked.ID, Root: OnDisk(root)},
		Manifest:   Manifest{ID: locked.ID, Name: locked.ID, Version: locked.Version},
		Kind:       locked.Kind,
		Locked:     &lockedCopy,
		OmitReason: reason,
		OmitDetail: detail,
	}
}

func isPathUnavailable(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}

func inspectLockedPackage(locked LockedPackage) (packInspection, error) {
	ins := packInspection{root: strings.TrimSpace(locked.Source)}
	root := ins.root
	var cachedMeta *PackageBodyMetadata
	if locked.Kind == PackKindGit {
		var err error
		root, err = CachedPackRevisionDir(locked.ID, locked.Revision)
		if err != nil {
			return ins, err
		}
		ins.root = root
		meta, err := ReadPackageBodyMetadata(root)
		if err != nil {
			return ins, fmt.Errorf("package provenance unavailable: %w", err)
		}
		if meta.PackID != locked.ID || meta.Version != locked.Version ||
			meta.ResolvedRevision != locked.Revision || meta.Integrity != locked.Integrity ||
			meta.ExtensionAPI == "" || meta.Kind != locked.Kind {
			return ins, fmt.Errorf("cached package provenance does not match lock")
		}
		cachedMeta = &meta
	}
	if _, err := os.Stat(root); err != nil {
		return ins, fmt.Errorf("package body unavailable: %w", err)
	}
	// One walk hashes and inventories so loaded bytes are the hashed bytes.
	walked, err := walkPackTree(root, locked.ID, true)
	if err != nil {
		return ins, err
	}
	if len(walked.Manifest) == 0 {
		return ins, fmt.Errorf("package body has no %s", config.PackManifestName)
	}
	man, err := ParseManifest(root, walked.Manifest)
	if err != nil {
		return ins, err
	}
	if man.ID != locked.ID || man.Version != locked.Version {
		return ins, fmt.Errorf("manifest identity %s@%s does not match lock", man.ID, man.Version)
	}
	if cachedMeta != nil && cachedMeta.ExtensionAPI != man.Compatibility.ExtensionAPI {
		return ins, fmt.Errorf("cached package API provenance does not match manifest")
	}
	lockedCopy := locked
	ins.integrity = walked.Integrity
	ins.content = PackContent{
		Pack:     Pack{ID: locked.ID, Root: OnDisk(root)},
		Manifest: man,
		Kind:     locked.Kind,
		Locked:   &lockedCopy,
		Units:    walked.Units,
	}
	return ins, nil
}
