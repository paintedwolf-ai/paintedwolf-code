package layeredfs

import (
	"fmt"
	"path/filepath"
	"strings"
)

func sanitizeRelPath(rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute relative path %q", ErrPathEscape, rel)
	}
	var stack []string
	for _, part := range strings.Split(rel, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, part)
		}
	}
	if len(stack) == 0 {
		return ".", nil
	}
	return strings.Join(stack, "/"), nil
}

// IsGitMetadataPath reports a path inside a repository's .git, which only private Git manages.
func IsGitMetadataPath(rel string) bool { return isGitComponentPath(rel) }

func isGitComponentPath(rel string) bool {
	rel = cleanRelPath(rel)
	if rel == ".git" || strings.HasPrefix(rel, ".git/") || strings.HasSuffix(rel, "/.git") {
		return true
	}
	return strings.Contains(rel, "/.git/")
}

func checkGitAccess(c *Core, qpath QualifiedPath, writeIntent bool) (QualifiedPath, error) {
	if !isGitComponentPath(qpath.Rel) {
		return qpath, nil
	}
	c.mu.RLock()
	_, isPrivate := c.state.pathIndex[qpath]
	interceptor := c.cfg.GitInterceptor
	allowPassthrough := c.cfg.GitAllowPassthrough
	c.mu.RUnlock()
	if isPrivate {
		return qpath, nil
	}
	if interceptor != nil {
		outcome, err := interceptor.InterceptGitAccess(c.cfg.ViewID, qpath, writeIntent)
		if err != nil {
			return qpath, err
		}
		if outcome.Intercepted && outcome.RedirectRel != "" {
			rootID := outcome.RedirectRoot
			if rootID == "" {
				rootID = qpath.RootID
			}
			return QualifiedPath{RootID: rootID, Rel: cleanRelPath(outcome.RedirectRel)}, nil
		}
		if outcome.Intercepted {
			return qpath, nil
		}
	}
	if allowPassthrough {
		return qpath, nil
	}
	return qpath, ErrGitPending
}

func nodeToAttrLocked(n *privateNode) EntryAttr {
	nlink := uint32(len(n.paths)) //nolint:gosec // A node has a handful of names.
	if n.kind == EntryDirectory && nlink < 2 {
		nlink = 2
	}
	return EntryAttr{
		NodeID:     n.id,
		OriginID:   n.origin,
		Generation: n.gen,
		Layer:      LayerPrivate,
		Kind:       n.kind,
		Mode:       n.mode,
		Size:       n.size,
		ModTime:    n.mtime,
		ATime:      n.atime,
		CTime:      n.ctime,
		Nlink:      nlink,
		LinkTarget: n.flags.linkTarget,
		Xattrs:     cloneXattrs(n.xattrs),
	}
}

func parentEntryToAttr(qpath QualifiedPath, pe ParentEntry) EntryAttr {
	return EntryAttr{
		NodeID:     syntheticNodeID(qpath, 1),
		Generation: 1,
		Layer:      LayerParent,
		Kind:       pe.Kind,
		Mode:       pe.Mode,
		Size:       pe.Size,
		ModTime:    pe.ModTime,
		ATime:      pe.ModTime,
		CTime:      pe.ModTime,
		Nlink:      1,
		LinkTarget: pe.LinkTarget,
		Xattrs:     cloneXattrs(pe.Xattrs),
	}
}

func lowerEntryToAttr(qpath QualifiedPath, le LowerEntry, xattrs map[string][]byte) EntryAttr {
	nlink := le.Nlink
	if nlink == 0 {
		nlink = 1
	}
	return EntryAttr{
		NodeID:     syntheticNodeID(qpath, le.Ino),
		Generation: 1,
		Layer:      LayerTrunk,
		Kind:       le.Kind,
		Mode:       le.Mode,
		Size:       le.Size,
		ModTime:    le.ModTime,
		ATime:      le.ModTime,
		CTime:      le.ModTime,
		Nlink:      nlink,
		LinkTarget: le.LinkTarget,
		Xattrs:     xattrs,
	}
}

func syntheticNodeID(qpath QualifiedPath, ino uint64) NodeID {
	var h uint64 = 14695981039346656037
	for _, b := range []byte(qpath.RootID + ":" + qpath.Rel) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	h ^= ino
	return NodeID((h & 0x7fffffffffffffff) | (1 << 62))
}
