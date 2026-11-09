package adapter

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func collectGrantIDs(grants []RootGrant) []string {
	ids := make([]string, 0, len(grants))
	for _, g := range grants {
		ids = append(ids, g.RootID)
	}
	return ids
}

func suggestSubroots(grants []RootGrant) []string {
	var suggestions []string
	for _, g := range grants {
		entries, err := os.ReadDir(g.SourcePath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != ".git" && !strings.HasPrefix(e.Name(), ".") {
				suggestions = append(suggestions, g.RootID+":"+e.Name())
			}
		}
	}
	sort.Strings(suggestions)
	return suggestions
}

// pristineLowerProvider serves a root's exact base to Core: names and modes from the stat manifest
// and bytes from the pristine tree, so a worker's edits in the working tree never reach a trunk lookup.
type pristineLowerProvider struct {
	pristineDir string
	records     map[string]statRecord
	children    map[string][]string
}

func newPristineLowerProvider(rootID, pristineDir string, all []statRecord) *pristineLowerProvider {
	records := make(map[string]statRecord)
	childSet := make(map[string]map[string]struct{})
	addChild := func(parent, name string) {
		if name == "" || name == "." {
			return
		}
		m := childSet[parent]
		if m == nil {
			m = make(map[string]struct{})
			childSet[parent] = m
		}
		m[name] = struct{}{}
	}
	for _, rec := range all {
		if rec.Root != rootID {
			continue
		}
		rel := cleanLowerRel(rec.Rel)
		if rel == "" {
			continue
		}
		records[rel] = rec
		cur := rel
		for {
			parent := path.Dir(cur)
			base := path.Base(cur)
			if parent == "." || parent == "" {
				addChild("", base)
				break
			}
			addChild(parent, base)
			cur = parent
		}
	}
	children := make(map[string][]string, len(childSet))
	for parent, m := range childSet {
		names := make([]string, 0, len(m))
		for name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		children[parent] = names
	}
	return &pristineLowerProvider{pristineDir: pristineDir, records: records, children: children}
}

func cleanLowerRel(rel string) string {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	if clean == "." {
		return ""
	}
	return clean
}

func (p *pristineLowerProvider) Lstat(rel string) (layeredfs.LowerEntry, error) {
	clean := cleanLowerRel(rel)
	if clean == "" {
		return layeredfs.LowerEntry{Kind: layeredfs.EntryDirectory, Mode: fs.ModeDir | 0o755, Nlink: 2}, nil
	}
	rec, ok := p.records[clean]
	if !ok {
		return layeredfs.LowerEntry{}, os.ErrNotExist
	}
	return layeredfs.LowerEntry{
		Kind: rec.Kind, Mode: rec.fileMode(), Size: rec.Size, ModTime: rec.modTime(), Nlink: 1, LinkTarget: rec.LinkTarget,
	}, nil
}

func (p *pristineLowerProvider) Readlink(rel string) (string, error) {
	rec, ok := p.records[cleanLowerRel(rel)]
	if !ok {
		return "", os.ErrNotExist
	}
	if rec.Kind != layeredfs.EntrySymlink {
		return "", os.ErrInvalid
	}
	return rec.LinkTarget, nil
}

func (p *pristineLowerProvider) ReadDir(rel string) ([]layeredfs.LowerDirEntry, error) {
	clean := cleanLowerRel(rel)
	if clean != "" {
		rec, ok := p.records[clean]
		if !ok {
			return nil, os.ErrNotExist
		}
		if rec.Kind != layeredfs.EntryDirectory {
			return nil, os.ErrInvalid
		}
	}
	names := p.children[clean]
	out := make([]layeredfs.LowerDirEntry, 0, len(names))
	for _, name := range names {
		childRel := name
		if clean != "" {
			childRel = clean + "/" + name
		}
		rec := p.records[childRel]
		out = append(out, layeredfs.LowerDirEntry{Name: name, Kind: rec.Kind, Mode: rec.fileMode()})
	}
	return out, nil
}

func (p *pristineLowerProvider) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	entry, err := p.Lstat(rel)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	if entry.Kind != layeredfs.EntryRegular {
		return nil, layeredfs.LowerEntry{}, os.ErrInvalid
	}
	f, err := contentOpen(filepath.Join(p.pristineDir, filepath.FromSlash(cleanLowerRel(rel))))
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	return f, entry, nil
}

func (p *pristineLowerProvider) ListXattr(rel string) (map[string][]byte, error) {
	clean := cleanLowerRel(rel)
	if clean == "" {
		return nil, nil
	}
	if _, ok := p.records[clean]; !ok {
		return nil, os.ErrNotExist
	}
	return readPathXattrs(filepath.Join(p.pristineDir, filepath.FromSlash(clean)))
}

func parentEntriesForRoot(pl layeredfs.ParentLayer, rootID string) (map[string]layeredfs.ParentEntry, error) {
	if snap, ok := pl.(interface{ EntriesForRoot(string) map[string]layeredfs.ParentEntry }); ok {
		return snap.EntriesForRoot(rootID), nil
	}
	out := make(map[string]layeredfs.ParentEntry)
	var walk func(dir string) error
	walk = func(dir string) error {
		children, err := pl.ListDir(layeredfs.QualifiedPath{RootID: rootID, Rel: dir})
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		for name, pe := range children {
			rel := name
			if dir != "." && dir != "" {
				rel = dir + "/" + name
			}
			out[rel] = pe
			if pe.Kind == layeredfs.EntryDirectory {
				if err := walk(rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return out, nil
}

func overlayParentLayersOnRoots(roots []PreparedRoot, parents []layeredfs.ParentLayer) error {
	for i := len(parents) - 1; i >= 0; i-- {
		pl := parents[i]
		for _, r := range roots {
			entries, err := parentEntriesForRoot(pl, r.RootID)
			if err != nil {
				return err
			}
			rels := make([]string, 0, len(entries))
			for rel, pe := range entries {
				if !pe.Synthetic {
					rels = append(rels, rel)
				}
			}
			sort.Strings(rels)
			for _, rel := range rels {
				if err := applyParentEntryToWorkspace(pl, layeredfs.QualifiedPath{RootID: r.RootID, Rel: rel}, filepath.Join(r.WorkspacePath, filepath.FromSlash(rel)), entries[rel]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func applyParentEntryToWorkspace(pl layeredfs.ParentLayer, qp layeredfs.QualifiedPath, dstPath string, pe layeredfs.ParentEntry) error {
	if pe.Kind == layeredfs.EntryAbsent {
		return os.RemoveAll(dstPath)
	}
	if info, err := os.Lstat(dstPath); err == nil && info.IsDir() != (pe.Kind == layeredfs.EntryDirectory) {
		if err := os.RemoveAll(dstPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	switch pe.Kind {
	case layeredfs.EntryDirectory:
		return os.MkdirAll(dstPath, pe.Mode.Perm()|0o700)
	case layeredfs.EntrySymlink:
		_ = os.RemoveAll(dstPath)
		return os.Symlink(pe.LinkTarget, dstPath)
	case layeredfs.EntryRegular:
		rc, err := pl.OpenContent(qp)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		raw, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		mode := pe.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		_ = os.RemoveAll(dstPath)
		if err := os.WriteFile(dstPath, raw, mode); err != nil {
			return err
		}
	}
	if len(pe.Xattrs) > 0 {
		if err := writePathXattrs(dstPath, pe.Xattrs); err != nil {
			return err
		}
	}
	if pe.Kind != layeredfs.EntrySymlink && !pe.ModTime.IsZero() {
		_ = os.Chtimes(dstPath, pe.ModTime, pe.ModTime)
	}
	return nil
}

func (s *statCapture) parentEntry(qp layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
	var synthetic layeredfs.ParentEntry
	var hasSynthetic bool
	for _, parent := range s.parents {
		pe, ok, err := parent.Lookup(qp)
		if err != nil {
			return layeredfs.ParentEntry{}, false, err
		}
		if !ok {
			continue
		}
		if pe.Synthetic {
			if !hasSynthetic {
				synthetic, hasSynthetic = pe, true
			}
			continue
		}
		return pe, true, nil
	}
	if hasSynthetic {
		if _, inManifest := s.manifest[qp]; !inManifest {
			return synthetic, true, nil
		}
	}
	return layeredfs.ParentEntry{}, false, nil
}

func (s *statCapture) hasEffectiveBase(qp layeredfs.QualifiedPath) (bool, error) {
	pe, ok, err := s.parentEntry(qp)
	if err != nil || ok {
		return ok && pe.Kind != layeredfs.EntryAbsent, err
	}
	_, inManifest := s.manifest[qp]
	return inManifest, nil
}

func (s *statCapture) seedParentCurrent(rootID string) error {
	for _, parent := range s.parents {
		entries, err := parentEntriesForRoot(parent, rootID)
		if err != nil {
			return err
		}
		for rel := range entries {
			qp := layeredfs.QualifiedPath{RootID: rootID, Rel: rel}
			if _, seen := s.current[qp]; seen {
				continue
			}
			resolved, ok, err := s.parentEntry(qp)
			if err != nil {
				return err
			}
			if ok && resolved.Kind != layeredfs.EntryAbsent && !resolved.Synthetic {
				s.current[qp] = statRecord{
					Root: rootID, Rel: rel, Kind: resolved.Kind, Mode: uint32(resolved.Mode),
					Size: resolved.Size, MtimeNS: resolved.ModTime.UnixNano(), LinkTarget: resolved.LinkTarget,
				}
			}
		}
	}
	return nil
}
