package adapter

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

func resolvePrepareLimits(maxEntries, maxBytes int64) (int64, int64) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return maxEntries, maxBytes
}

func collectGrantIDs(grants []RootGrant) []string {
	ids := make([]string, 0, len(grants))
	for _, g := range grants {
		ids = append(ids, g.RootID)
	}
	return ids
}

func checkSourceIndexEstimates(viewID string, narrowed []RootGrant, selectedIDs []string, maxEntries, maxBytes int64) error {
	var totalEntries, totalBytes int64
	for _, g := range narrowed {
		if g.Estimate == nil {
			continue
		}
		totalEntries += g.Estimate.EstimatedEntries
		totalBytes += g.Estimate.EstimatedBytes
		if g.Estimate.EstimatedEntries > maxEntries || g.Estimate.EstimatedBytes > maxBytes ||
			totalEntries > maxEntries || totalBytes > maxBytes {
			return &PreparationLimitError{
				ReasonCode: ReasonWorkspacePreparationLimit, ViewID: viewID, RootID: g.RootID,
				SelectedRoots: selectedIDs, ObservedEntries: totalEntries, ObservedBytes: totalBytes,
				MaxEntries: maxEntries, MaxBytes: maxBytes, EstimatedRejection: true,
				CleanedIncompleteSeed: true, RetryWithSmallerSelection: true,
				SuggestedSubroots: suggestSubroots(narrowed),
				Diagnostic:        "source-index estimate exceeds workspace preparation limit",
			}
		}
	}
	return nil
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

// seededLowerProvider serves the immutable initial materialized seed snapshot to Core
// so direct on-disk edits/deletions/replacements in the materialized workspace directory
// never corrupt Core's LayerTrunk lookups during journal synchronization.
type seededLowerProvider struct {
	rootID   string
	seedDir  string
	bases    map[string]layeredfs.BaseCapture
	children map[string][]string
}

func newSeededLowerProvider(rootID, seedDir string, allBases []layeredfs.BaseCapture) *seededLowerProvider {
	bases := make(map[string]layeredfs.BaseCapture)
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
	for _, b := range allBases {
		if b.Path.RootID != rootID {
			continue
		}
		rel := strings.TrimPrefix(path.Clean(strings.ReplaceAll(b.Path.Rel, "\\", "/")), "/")
		if rel == "" || rel == "." {
			continue
		}
		bases[rel] = b
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
	return &seededLowerProvider{
		rootID:   rootID,
		seedDir:  seedDir,
		bases:    bases,
		children: children,
	}
}

func registerSeededLowerProviders(
	core *layeredfs.Core,
	roots []PreparedRoot,
	bases []layeredfs.BaseCapture,
	_ map[layeredfs.QualifiedPath]string,
) error {
	if core == nil {
		return nil
	}
	for _, r := range roots {
		if err := core.RegisterLowerProvider(r.RootID, newSeededLowerProvider(r.RootID, r.WorkspacePath, bases)); err != nil {
			return err
		}
	}
	return nil
}

func (p *seededLowerProvider) Lstat(rel string) (layeredfs.LowerEntry, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	if clean == "" || clean == "." {
		return layeredfs.LowerEntry{
			Kind:  layeredfs.EntryDirectory,
			Mode:  fs.ModeDir | 0o755,
			Nlink: 2,
		}, nil
	}
	b, ok := p.bases[clean]
	if !ok {
		return layeredfs.LowerEntry{}, os.ErrNotExist
	}
	return layeredfs.LowerEntry{
		Kind:       b.Kind,
		Mode:       b.Mode,
		Size:       b.Size,
		ModTime:    b.ModTime,
		Ino:        b.Ino,
		Nlink:      1,
		LinkTarget: b.LinkTarget,
	}, nil
}

func (p *seededLowerProvider) Readlink(rel string) (string, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	b, ok := p.bases[clean]
	if !ok {
		return "", os.ErrNotExist
	}
	if b.Kind != layeredfs.EntrySymlink {
		return "", os.ErrInvalid
	}
	return b.LinkTarget, nil
}

func (p *seededLowerProvider) ReadDir(rel string) ([]layeredfs.LowerDirEntry, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	if clean == "." {
		clean = ""
	}
	if clean != "" {
		b, ok := p.bases[clean]
		if !ok {
			return nil, os.ErrNotExist
		}
		if b.Kind != layeredfs.EntryDirectory {
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
		b := p.bases[childRel]
		out = append(out, layeredfs.LowerDirEntry{
			Name: name,
			Kind: b.Kind,
			Mode: b.Mode,
			Ino:  b.Ino,
		})
	}
	return out, nil
}

func (p *seededLowerProvider) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	entry, err := p.Lstat(rel)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	if entry.Kind != layeredfs.EntryRegular {
		return nil, layeredfs.LowerEntry{}, os.ErrInvalid
	}
	f, err := os.Open(filepath.Join(p.seedDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	return f, entry, nil
}

func (p *seededLowerProvider) ListXattr(rel string) (map[string][]byte, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "/")
	if clean == "" || clean == "." {
		return nil, nil
	}
	b, ok := p.bases[clean]
	if !ok {
		return nil, os.ErrNotExist
	}
	if len(b.Xattrs) == 0 {
		return nil, nil
	}
	cp := make(map[string][]byte, len(b.Xattrs))
	for k, v := range b.Xattrs {
		cp[k] = append([]byte(nil), v...)
	}
	return cp, nil
}

func readPathXattrs(p string) (map[string][]byte, error) {
	sz, err := unix.Llistxattr(p, nil)
	if err != nil || sz <= 0 {
		return nil, nil
	}
	buf := make([]byte, sz)
	n, err := unix.Llistxattr(p, buf)
	if err != nil || n <= 0 {
		return nil, nil
	}
	out := make(map[string][]byte)
	for _, rawName := range bytes.Split(buf[:n], []byte{0}) {
		if len(rawName) == 0 {
			continue
		}
		name := string(rawName)
		vsz, gErr := unix.Lgetxattr(p, name, nil)
		if gErr != nil || vsz < 0 {
			continue
		}
		if vsz == 0 {
			out[name] = []byte{}
			continue
		}
		vbuf := make([]byte, vsz)
		vn, gErr := unix.Lgetxattr(p, name, vbuf)
		if gErr == nil && vn >= 0 {
			out[name] = vbuf[:vn]
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
