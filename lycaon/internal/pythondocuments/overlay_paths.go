package pythondocuments

import (
	"context"
	"io/fs"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func (a *OverlayAdapter) resolvePath(raw string) (pythonruntime.CanonicalTarget, RootSpec, pythonruntime.Errno) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoENOENT
	}
	if raw == "/dev/null" {
		return pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceNullDevice}, RootSpec{}, pythonruntime.ErrnoSuccess
	}
	if target, root, ok, errno := a.resolveSpecialPrefix(raw); ok || errno != pythonruntime.ErrnoSuccess {
		return target, root, errno
	}

	// Attached root addressing: @label/sub or /@label/sub
	trimmed := strings.TrimPrefix(raw, "/")
	if strings.HasPrefix(trimmed, "@") {
		rest := strings.TrimPrefix(trimmed, "@")
		label, sub, _ := strings.Cut(rest, "/")
		root, ok := a.rootsByLbl[label]
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoENOENT
		}
		cleaned, ok := cleanConfinedRel(sub)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    root.ID,
			Branch:    root.Branch,
			RelPath:   cleaned,
		}, root, pythonruntime.ErrnoSuccess
	}

	// Default workspace root via /workspace/... or relative path against CWD.
	if raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") {
		rel := strings.TrimPrefix(strings.TrimPrefix(raw, "/workspace"), "/")
		cleaned, ok := cleanConfinedRel(rel)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    a.defaultRoot.ID,
			Branch:    a.defaultRoot.Branch,
			RelPath:   cleaned,
		}, a.defaultRoot, pythonruntime.ErrnoSuccess
	}

	if strings.HasPrefix(raw, "/") {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
	}

	// Paths arriving from WASI (WorkspaceSysFS) are already resolved relative to
	// the /workspace mount root by WASI + guest os.chdir(cwd), so do not prepend
	// a.cwd a second time.
	cleaned, ok := cleanConfinedRel(raw)
	if !ok {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
	}
	return pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    a.defaultRoot.ID,
		Branch:    a.defaultRoot.Branch,
		RelPath:   cleaned,
	}, a.defaultRoot, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) resolveSpecialPrefix(raw string) (pythonruntime.CanonicalTarget, RootSpec, bool, pythonruntime.Errno) {
	specs := []struct {
		prefix string
		ns     pythonruntime.Namespace
		id     string
		ro     bool
	}{
		{"/scratch", pythonruntime.NamespaceScratch, "scratch", false},
		{"/runtime", pythonruntime.NamespaceRuntime, "runtime", true},
		{"/packages", pythonruntime.NamespacePackages, "packages", true},
	}
	for _, sp := range specs {
		if raw == sp.prefix || strings.HasPrefix(raw, sp.prefix+"/") {
			rel := strings.TrimPrefix(strings.TrimPrefix(raw, sp.prefix), "/")
			cleaned, ok := cleanConfinedRel(rel)
			if !ok {
				return pythonruntime.CanonicalTarget{}, RootSpec{}, true, pythonruntime.ErrnoEACCES
			}
			return pythonruntime.CanonicalTarget{
				Namespace: sp.ns,
				RootID:    sp.id,
				RelPath:   cleaned,
			}, RootSpec{ID: sp.id, Label: sp.id, ReadOnly: sp.ro}, true, pythonruntime.ErrnoSuccess
		}
	}
	return pythonruntime.CanonicalTarget{}, RootSpec{}, false, pythonruntime.ErrnoSuccess
}

func cleanConfinedRel(rel string) (string, bool) {
	if rel == "" || rel == "." {
		return "", true
	}
	cleaned := path.Clean(rel)
	if cleaned == "." {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return strings.TrimPrefix(cleaned, "/"), true
}

func (a *OverlayAdapter) followSymlinksLocked(ctx context.Context, target pythonruntime.CanonicalTarget, followFinal bool) (pythonruntime.CanonicalTarget, pythonruntime.Errno) {
	if target.Namespace != pythonruntime.NamespaceWorkspace && target.Namespace != pythonruntime.NamespaceScratch {
		return target, pythonruntime.ErrnoSuccess
	}
	cur := target
	for hops := 0; hops < maxSymlinkHops; hops++ {
		if cur.RelPath == "" {
			return cur, pythonruntime.ErrnoSuccess
		}
		parts := strings.Split(cur.RelPath, "/")
		followed := false
		prefix := ""
		for i, part := range parts {
			if prefix == "" {
				prefix = part
			} else {
				prefix += "/" + part
			}
			if i == len(parts)-1 && !followFinal {
				break
			}
			probe := cur
			probe.RelPath = prefix
			linkTarget, isLink, errno := a.readlinkSingleLocked(ctx, probe)
			if errno != pythonruntime.ErrnoSuccess {
				if errno == pythonruntime.ErrnoENOENT {
					return cur, pythonruntime.ErrnoSuccess
				}
				return pythonruntime.CanonicalTarget{}, errno
			}
			if !isLink {
				continue
			}
			var base string
			if strings.HasPrefix(linkTarget, "/") {
				base = strings.TrimPrefix(linkTarget, "/")
			} else {
				parent := path.Dir(prefix)
				if parent == "." {
					base = linkTarget
				} else {
					base = parent + "/" + linkTarget
				}
			}
			if i+1 < len(parts) {
				remaining := strings.Join(parts[i+1:], "/")
				if base == "" {
					base = remaining
				} else {
					base = base + "/" + remaining
				}
			}
			cleaned, ok := cleanConfinedRel(base)
			if !ok {
				return pythonruntime.CanonicalTarget{}, pythonruntime.ErrnoEACCES
			}
			cur.RelPath = cleaned
			followed = true
			break
		}
		if !followed {
			return cur, pythonruntime.ErrnoSuccess
		}
	}
	return pythonruntime.CanonicalTarget{}, pythonruntime.ErrnoEINVAL
}

func (a *OverlayAdapter) readlinkSingleLocked(ctx context.Context, target pythonruntime.CanonicalTarget) (string, bool, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		n, ok := a.scratch[k]
		if !ok || n.kind == nodeTombstone {
			return "", false, pythonruntime.ErrnoENOENT
		}
		return n.linkTarget, n.kind == nodeSymlink, pythonruntime.ErrnoSuccess
	}
	if n, ok := a.overlay[k]; ok {
		if n.kind == nodeTombstone {
			return "", false, pythonruntime.ErrnoENOENT
		}
		return n.linkTarget, n.kind == nodeSymlink, pythonruntime.ErrnoSuccess
	}
	if a.store == nil {
		return "", false, pythonruntime.ErrnoENOENT
	}
	snap, errno := a.store.StatDocument(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return "", false, errno
	}
	return snap.LinkTarget, snap.IsSymlink, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) ensureParentDirLocked(ctx context.Context, target pythonruntime.CanonicalTarget) pythonruntime.Errno {
	if target.RelPath == "" {
		return pythonruntime.ErrnoSuccess
	}
	parent := path.Dir(target.RelPath)
	if parent == "." || parent == "" {
		return pythonruntime.ErrnoSuccess
	}
	pt := target
	pt.RelPath = parent
	info, errno := a.statTargetLocked(ctx, pt)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if !info.IsDir {
		return pythonruntime.ErrnoENOTDIR
	}
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) statTargetLocked(ctx context.Context, target pythonruntime.CanonicalTarget) (pythonruntime.StatInfo, pythonruntime.Errno) {
	switch target.Namespace {
	case pythonruntime.NamespaceNullDevice:
		return pythonruntime.StatInfo{
			Target:  target,
			Name:    "null",
			Mode:    fs.ModeDevice | fs.ModeCharDevice | 0o666,
			ModTime: a.now().UTC(),
		}, pythonruntime.ErrnoSuccess
	case pythonruntime.NamespaceRuntime, pythonruntime.NamespacePackages:
		if data, ok := a.roFiles[CanonicalKeyOf(target)]; ok {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    path.Base(target.RelPath),
				Size:    int64(len(data)),
				Mode:    0o444,
				ModTime: a.now().UTC(),
			}, pythonruntime.ErrnoSuccess
		}
		if target.RelPath == "" {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    string(target.Namespace),
				Mode:    fs.ModeDir | 0o555,
				ModTime: a.now().UTC(),
				IsDir:   true,
			}, pythonruntime.ErrnoSuccess
		}
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
	case pythonruntime.NamespaceScratch:
		if target.RelPath == "" {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    "scratch",
				Mode:    fs.ModeDir | 0o755,
				ModTime: a.now().UTC(),
				IsDir:   true,
				Inode:   2,
			}, pythonruntime.ErrnoSuccess
		}
		n, ok := a.scratch[CanonicalKeyOf(target)]
		if !ok || n.kind == nodeTombstone {
			return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
		}
		return nodeStatInfo(n), pythonruntime.ErrnoSuccess
	}

	if target.RelPath == "" {
		return pythonruntime.StatInfo{
			Target:  target,
			Name:    target.RootID,
			Mode:    fs.ModeDir | 0o755,
			ModTime: a.now().UTC(),
			IsDir:   true,
			Inode:   1,
		}, pythonruntime.ErrnoSuccess
	}
	if n, ok := a.overlay[CanonicalKeyOf(target)]; ok {
		if n.kind == nodeTombstone {
			return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
		}
		return nodeStatInfo(n), pythonruntime.ErrnoSuccess
	}
	if a.store == nil {
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
	}
	snap, errno := a.store.StatDocument(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return pythonruntime.StatInfo{}, errno
	}
	return snapshotStatInfo(snap), pythonruntime.ErrnoSuccess
}

func nodeStatInfo(n *overlayNode) pythonruntime.StatInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	name := path.Base(n.target.RelPath)
	if n.target.RelPath == "" {
		name = n.target.RootID
	}
	return pythonruntime.StatInfo{
		Target:    n.target,
		Name:      name,
		Size:      int64(len(n.data)),
		Mode:      n.mode,
		ModTime:   n.modTime,
		IsDir:     n.kind == nodeDir,
		IsSymlink: n.kind == nodeSymlink,
		Inode:     n.inode,
	}
}

func snapshotStatInfo(snap DocumentSnapshot) pythonruntime.StatInfo {
	name := path.Base(snap.Target.RelPath)
	if snap.Target.RelPath == "" {
		name = snap.Target.RootID
	}
	return pythonruntime.StatInfo{
		Target:    snap.Target,
		Name:      name,
		Size:      int64(len(snap.Bytes)),
		Mode:      snap.Mode,
		ModTime:   snap.ModTime,
		IsDir:     snap.IsDir,
		IsSymlink: snap.IsSymlink,
		Inode:     snap.Inode,
	}
}
