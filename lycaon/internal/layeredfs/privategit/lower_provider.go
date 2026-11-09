package privategit

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

type gitAwareLowerProvider struct {
	mgr      *Manager
	viewID   string
	rootID   string
	trunkDir string
}

func (p *gitAwareLowerProvider) resolveBackingTarget(rel string) (string, bool, error) {
	rel = cleanSlashRel(rel)
	if repoRel, subRel, isGit := splitGitRelPath(rel); isGit {
		gitDir, err := readyPrivateGitDir(p.mgr, p.viewID, repoRel)
		if err != nil {
			return "", true, err
		}
		if subRel == "." || subRel == "" {
			return gitDir, true, nil
		}
		target := filepath.Join(gitDir, filepath.FromSlash(subRel))
		return target, true, nil
	}
	if rel == "." || rel == "" {
		return p.trunkDir, false, nil
	}
	return filepath.Join(p.trunkDir, filepath.FromSlash(rel)), false, nil
}

func (p *gitAwareLowerProvider) ResolveMutableGitTarget(rel string) (string, bool, bool, error) {
	rel = cleanSlashRel(rel)
	repoRel, subRel, isGit := splitGitRelPath(rel)
	if !isGit {
		return "", false, false, nil
	}
	gitDir, err := readyPrivateGitDir(p.mgr, p.viewID, repoRel)
	if err != nil {
		return "", false, true, err
	}
	if subRel == "." || subRel == "" {
		return gitDir, true, true, nil
	}
	return filepath.Join(gitDir, filepath.FromSlash(subRel)), false, true, nil
}

func (p *gitAwareLowerProvider) Lstat(rel string) (layeredfs.LowerEntry, error) {
	target, _, err := p.resolveBackingTarget(rel)
	if err != nil {
		return layeredfs.LowerEntry{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return layeredfs.LowerEntry{}, err
	}
	entry := fileInfoToLowerEntry(info)
	if entry.Kind == layeredfs.EntrySymlink {
		link, lErr := os.Readlink(target)
		if lErr != nil {
			return layeredfs.LowerEntry{}, lErr
		}
		entry.LinkTarget = link
	}
	return entry, nil
}

func (p *gitAwareLowerProvider) ReadDir(rel string) ([]layeredfs.LowerDirEntry, error) {
	target, isGit, err := p.resolveBackingTarget(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	out := make([]layeredfs.LowerDirEntry, 0, len(entries))
	for _, de := range entries {
		info, iErr := de.Info()
		if iErr != nil {
			continue
		}
		le := fileInfoToLowerEntry(info)
		if !isGit && de.Name() == ".git" {
			le.Kind = layeredfs.EntryDirectory
			le.Mode = fs.ModeDir | 0o755
		}
		out = append(out, layeredfs.LowerDirEntry{
			Name: de.Name(),
			Kind: le.Kind,
			Mode: le.Mode,
			Ino:  le.Ino,
		})
	}
	return out, nil
}

func (p *gitAwareLowerProvider) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	target, _, err := p.resolveBackingTarget(rel)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, layeredfs.LowerEntry{}, layeredfs.ErrSymlinkEscape
	}
	if info.IsDir() {
		return nil, layeredfs.LowerEntry{}, syscall.EISDIR
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	return f, fileInfoToLowerEntry(info), nil
}

func (p *gitAwareLowerProvider) Readlink(rel string) (string, error) {
	target, _, err := p.resolveBackingTarget(rel)
	if err != nil {
		return "", err
	}
	return os.Readlink(target)
}

func (p *gitAwareLowerProvider) ListXattr(_ string) (map[string][]byte, error) {
	return nil, nil
}

func fileInfoToLowerEntry(info fs.FileInfo) layeredfs.LowerEntry {
	kind := layeredfs.EntryRegular
	mode := info.Mode()
	switch {
	case mode.IsDir():
		kind = layeredfs.EntryDirectory
	case mode&fs.ModeSymlink != 0:
		kind = layeredfs.EntrySymlink
	}
	le := layeredfs.LowerEntry{
		Kind:      kind,
		Mode:      mode,
		Size:      info.Size(),
		ModTime:   info.ModTime(),
		CtimeSec:  info.ModTime().Unix(),
		CtimeNsec: int64(info.ModTime().Nanosecond()),
		Nlink:     1,
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		le.Dev = uint64(st.Dev)
		le.Ino = uint64(st.Ino)
		le.UID = st.Uid
		le.GID = st.Gid
		le.Nlink = uint32(st.Nlink)
	}
	return le
}

func splitGitRelPath(rel string) (repoRel, subRel string, isGit bool) {
	rel = cleanSlashRel(rel)
	if rel == ".git" {
		return ".", ".", true
	}
	if strings.HasPrefix(rel, ".git/") {
		return ".", strings.TrimPrefix(rel, ".git/"), true
	}
	if strings.HasSuffix(rel, "/.git") {
		return strings.TrimSuffix(rel, "/.git"), ".", true
	}
	if idx := strings.Index(rel, "/.git/"); idx >= 0 {
		return rel[:idx], rel[idx+len("/.git/"):], true
	}
	return "", "", false
}

func cleanSlashRel(rel string) string {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "/" {
		return "."
	}
	return rel
}

func registerSeedBaseIfCore(
	core *layeredfs.Core,
	rel string,
	kind layeredfs.EntryKind,
	mode fs.FileMode,
	size int64,
	mtime time.Time,
	sha, linkTarget string,
) error {
	if core == nil {
		return nil
	}
	return core.RegisterSeedBase(layeredfs.BaseCapture{
		Path:       layeredfs.QualifiedPath{RootID: "primary", Rel: rel},
		Layer:      layeredfs.LayerTrunk,
		Kind:       kind,
		Mode:       mode,
		Size:       size,
		ModTime:    mtime,
		SHA256:     sha,
		LinkTarget: linkTarget,
	})
}

func copyWorkingTreeFile(src, dst string, mode fs.FileMode) error {
	_, err := copyAndHashWorkingFile(src, dst, mode)
	return err
}

func copyAndHashWorkingFile(src, dst string, mode fs.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm|0o200)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, h), in)
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
