package adapter

import (
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
