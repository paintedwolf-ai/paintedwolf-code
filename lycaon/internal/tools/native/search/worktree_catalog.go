package search

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/tools"
)

type memoizedCheckout struct {
	binding gitrepo.CheckoutBinding
	ok      bool
}

type worktreeIdentityMemo struct {
	resolver    *gitrepo.CheckoutResolver
	memo        map[string]memoizedCheckout
	resolutions int64
	matches     int64
}

func newWorktreeIdentityMemo(tc tools.ToolContext) *worktreeIdentityMemo {
	var roots []gitrepo.AttachedRoot
	for _, r := range tc.Source.Roots {
		if r.External || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Path) == "" {
			continue
		}
		roots = append(roots, gitrepo.AttachedRoot{ID: r.ID, Path: r.Path})
	}
	return &worktreeIdentityMemo{
		resolver: gitrepo.NewCheckoutResolver(roots),
		memo:     map[string]memoizedCheckout{},
	}
}

func (m *worktreeIdentityMemo) bindOperand(e *entry) {
	if m == nil || !m.resolver.HasRepositories() {
		return
	}
	res := e.bound.Resolved
	if !res.External || !res.Root.External || res.ToolOutput || res.Compressed {
		return
	}
	admittedDir := res.Root.Path
	if admittedDir == "" || e.bound.AdmitPathRead(admittedDir) != nil {
		return
	}
	targetDir := res.Abs
	if !e.dir() {
		targetDir = filepath.Dir(res.Abs)
		if e.bound.AdmitPathRead(targetDir) != nil {
			return
		}
	}
	binding, ok := m.resolveDir(targetDir, admittedDir)
	if !ok {
		return
	}
	cp := binding
	e.checkout = &cp
	if addr, projected := binding.Project(res.Abs); projected {
		e.catalog = &addr
	}
}

func (m *worktreeIdentityMemo) enterDirectory(e *entry) {
	if e.depth == 0 || !e.bound.Resolved.External {
		return
	}
	if e.checkout == nil && e.catalog == nil {
		return
	}
	if gitrepo.IsRoot(e.bound.Resolved.Abs) {
		e.checkout = nil
		e.catalog = nil
		admittedDir := e.bound.Resolved.Root.Path
		if admittedDir == "" || e.bound.AdmitPathRead(admittedDir) != nil {
			return
		}
		if binding, ok := m.resolveDir(e.bound.Resolved.Abs, admittedDir); ok {
			cp := binding
			e.checkout = &cp
			if addr, projected := binding.Project(e.bound.Resolved.Abs); projected {
				e.catalog = &addr
			}
		}
		return
	}
	if e.catalog != nil {
		if !e.catalog.Valid() {
			e.checkout = nil
			e.catalog = nil
		}
		return
	}
	if e.checkout != nil {
		if addr, projected := e.checkout.Project(e.bound.Resolved.Abs); projected {
			e.catalog = &addr
		}
	}
}

func inheritChildCatalog(parent entry, child *entry) {
	child.checkout = parent.checkout
	if parent.catalog != nil {
		addr := parent.catalog.Child(child.name)
		child.catalog = &addr
		return
	}
	if parent.checkout != nil && !child.dir() {
		if addr, projected := parent.checkout.Project(child.bound.Resolved.Abs); projected {
			child.catalog = &addr
		}
	}
}

func (m *worktreeIdentityMemo) resolveDir(targetDir, admittedDir string) (gitrepo.CheckoutBinding, bool) {
	targetCanon := gitrepo.CanonicalDir(targetDir)
	admittedCanon := gitrepo.CanonicalDir(admittedDir)
	if targetCanon == "" || admittedCanon == "" {
		return gitrepo.CheckoutBinding{}, false
	}
	var visited []string
	for cur := targetCanon; ; {
		key := admittedCanon + "\x00" + cur
		if cached, found := m.memo[key]; found {
			for _, step := range visited {
				m.memo[step] = cached
			}
			return cached.binding, cached.ok
		}
		visited = append(visited, key)
		_, lstatErr := os.Lstat(gitrepo.WorktreeEntry(cur))
		if lstatErr == nil {
			result := m.compute(cur, admittedCanon)
			for _, step := range visited {
				m.memo[step] = result
			}
			return result.binding, result.ok
		}
		if !errors.Is(lstatErr, fs.ErrNotExist) {
			neg := memoizedCheckout{}
			for _, step := range visited {
				m.memo[step] = neg
			}
			return gitrepo.CheckoutBinding{}, false
		}
		if cur == admittedCanon {
			result := m.compute(admittedCanon, admittedCanon)
			for _, step := range visited {
				m.memo[step] = result
			}
			return result.binding, result.ok
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return gitrepo.CheckoutBinding{}, false
		}
		cur = parent
	}
}

func (m *worktreeIdentityMemo) compute(dirCanon, admittedCanon string) memoizedCheckout {
	m.resolutions++
	binding, ok := m.resolver.Resolve(dirCanon, admittedCanon)
	if ok {
		m.matches++
	}
	return memoizedCheckout{binding: binding, ok: ok}
}
