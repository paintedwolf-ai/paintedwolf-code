package sourceledger

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

type linkedResolver = gitrepo.CheckoutResolver

func linkedCheckoutResolver(roots []RootSpec) *gitrepo.CheckoutResolver {
	attached := make([]gitrepo.AttachedRoot, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root.ID) == "" || strings.TrimSpace(root.Path) == "" {
			continue
		}
		attached = append(attached, gitrepo.AttachedRoot{ID: root.ID, Path: root.Path})
	}
	return gitrepo.NewCheckoutResolver(attached)
}

func (o *outsideObservation) invalidateAllLinked(resolver *gitrepo.CheckoutResolver) {
	if o == nil || !resolver.HasRepositories() {
		return
	}
	o.mu.Lock()
	paths := append([]string(nil), o.rawPaths...)
	o.mu.Unlock()
	for _, raw := range paths {
		invalidateLinkedFolder(resolver, raw)
	}
}

func invalidateLinkedFolder(resolver *gitrepo.CheckoutResolver, folderPath string) {
	if !resolver.HasRepositories() || folderPath == "" {
		return
	}
	if addr, ok := resolver.ResolvePath(folderPath); ok {
		if addr.Rel == "" || addr.Rel == "." {
			sourcecatalog.Process().InvalidateRoot(addr.RootPath)
			return
		}
		sourcecatalog.Process().InvalidateRoot(addr.RootPath, addr.Rel)
	}
}

func invalidateLinkedChanges(resolver *gitrepo.CheckoutResolver, folderPath string, changedAbs []string, boundaries []sourcesnapshot.Boundary) {
	if !resolver.HasRepositories() {
		return
	}
	type batch struct {
		full  bool
		paths []string
		seen  map[string]struct{}
	}
	grouped := map[string]*batch{}
	var order []string
	record := func(absPath string) {
		addr, ok := resolver.ResolvePath(absPath)
		if !ok || addr.RootPath == "" {
			return
		}
		b := grouped[addr.RootPath]
		if b == nil {
			b = &batch{seen: map[string]struct{}{}}
			grouped[addr.RootPath] = b
			order = append(order, addr.RootPath)
		}
		if addr.Rel == "" || addr.Rel == "." {
			b.full = true
			return
		}
		if _, ok := b.seen[addr.Rel]; !ok {
			b.seen[addr.Rel] = struct{}{}
			b.paths = append(b.paths, addr.Rel)
		}
	}
	for _, abs := range changedAbs {
		record(abs)
	}
	for _, boundary := range boundaries {
		if boundary.Path == "" || boundary.Path == "." {
			record(folderPath)
			continue
		}
		record(filepath.Join(folderPath, filepath.FromSlash(boundary.Path)))
	}
	for _, base := range order {
		b := grouped[base]
		if b.full {
			sourcecatalog.Process().InvalidateRoot(base)
			continue
		}
		sourcecatalog.Process().InvalidateRoot(base, b.paths...)
	}
}
