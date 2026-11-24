package gitrepo

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

const (
	maxPointerBytes   = 8192
	maxWorktreeAdmins = 1024
	gitDirLinePrefix  = "gitdir: "
)

// AttachedRoot names an admitted project root that may anchor linked checkouts.
type AttachedRoot struct {
	ID   string
	Path string
}

// CatalogBase maps one permitted checkout subtree to its originating attached root.
type CatalogBase struct {
	RootID       string
	CheckoutRoot string
	BasePath     string
	BaseIdentity string
}

// CatalogAddress locates one entry within a verified catalog base.
type CatalogAddress struct {
	RootID       string
	CheckoutRoot string
	RootPath     string
	RootIdentity string
	Rel          string
}

// Child derives the address of an immediate child entry.
func (a CatalogAddress) Child(name string) CatalogAddress {
	next := a
	if a.Rel == "" || a.Rel == "." {
		next.Rel = name
	} else {
		next.Rel = a.Rel + "/" + name
	}
	return next
}

// Valid rechecks that the physical catalog base directory has not been replaced.
func (a CatalogAddress) Valid() bool {
	if a.RootID == "" || a.RootPath == "" || a.RootIdentity == "" {
		return false
	}
	current, err := fspath.EntryIdentity(a.RootPath + string(filepath.Separator) + ".")
	return err == nil && current == a.RootIdentity
}

// CheckoutBinding holds the verified checkout and its permitted catalog bases.
type CheckoutBinding struct {
	CheckoutRoot string
	Bases        []CatalogBase
}

// Project resolves absPath onto the unique most specific catalog base containing it.
func (b CheckoutBinding) Project(absPath string) (CatalogAddress, bool) {
	if len(b.Bases) == 0 {
		return CatalogAddress{}, false
	}
	cand := CanonicalDir(absPath)
	if cand == "" {
		return CatalogAddress{}, false
	}
	var best CatalogBase
	var bestRel string
	found := false
	ambiguous := false
	for _, base := range b.Bases {
		rel, ok := relWithin(base.BasePath, cand)
		if !ok {
			continue
		}
		if !found {
			best, bestRel, found = base, rel, true
			continue
		}
		switch {
		case base.BasePath == best.BasePath:
			if base.RootID != best.RootID {
				ambiguous = true
			}
		case isStrictDescendant(best.BasePath, base.BasePath):
			best, bestRel, ambiguous = base, rel, false
		case isStrictDescendant(base.BasePath, best.BasePath):
		default:
			ambiguous = true
		}
	}
	if !found || ambiguous {
		return CatalogAddress{}, false
	}
	addr := CatalogAddress{
		RootID:       best.RootID,
		CheckoutRoot: best.CheckoutRoot,
		RootPath:     best.BasePath,
		RootIdentity: best.BaseIdentity,
		Rel:          bestRel,
	}
	if !addr.Valid() {
		return CatalogAddress{}, false
	}
	return addr, true
}

type trustedAttachment struct {
	rootID    string
	repoRoot  string
	repoRel   string
	commonDir string
	commonID  os.FileInfo
	worktrees string
}

// CheckoutResolver matches external paths against attached repositories.
type CheckoutResolver struct {
	attached []trustedAttachment
}

// NewCheckoutResolver anchors the Git common directories of attached source roots.
func NewCheckoutResolver(roots []AttachedRoot) *CheckoutResolver {
	r := &CheckoutResolver{}
	for _, root := range roots {
		id := strings.TrimSpace(root.ID)
		if id == "" || strings.TrimSpace(root.Path) == "" {
			continue
		}
		if att, ok := anchorAttachment(id, root.Path); ok {
			r.attached = append(r.attached, att)
		}
	}
	return r
}

// HasRepositories reports whether any attached root belongs to a Git repository.
func (r *CheckoutResolver) HasRepositories() bool {
	return r != nil && len(r.attached) > 0
}

// Resolve establishes linked-worktree membership for targetDir within admittedDir.
// It never walks above admittedDir to inspect untrusted metadata.
func (r *CheckoutResolver) Resolve(targetDir, admittedDir string) (CheckoutBinding, bool) {
	if !r.HasRepositories() {
		return CheckoutBinding{}, false
	}
	targetCanon := CanonicalDir(targetDir)
	admittedCanon := CanonicalDir(admittedDir)
	if targetCanon == "" || admittedCanon == "" {
		return CheckoutBinding{}, false
	}
	if _, ok := relWithin(admittedCanon, targetCanon); !ok {
		return CheckoutBinding{}, false
	}
	if info, err := os.Stat(admittedCanon); err != nil || !info.IsDir() {
		return CheckoutBinding{}, false
	}
	if info, err := os.Stat(targetCanon); err != nil || !info.IsDir() {
		return CheckoutBinding{}, false
	}
	checkoutRoot, matched, walkedToTop := r.findWithinAdmitted(targetCanon, admittedCanon)
	if !walkedToTop {
		if len(matched) == 0 {
			return CheckoutBinding{}, false
		}
		return buildCheckoutBinding(checkoutRoot, admittedCanon, matched)
	}
	checkoutRoot, matched = r.findViaTrustedWorktrees(admittedCanon, true)
	if len(matched) == 0 {
		return CheckoutBinding{}, false
	}
	return buildCheckoutBinding(checkoutRoot, admittedCanon, matched)
}

// ResolvePath maps a mutated host path onto its registered linked checkout base
// using trusted repository registrations without walking untrusted ancestors.
func (r *CheckoutResolver) ResolvePath(absPath string) (CatalogAddress, bool) {
	if !r.HasRepositories() {
		return CatalogAddress{}, false
	}
	cand := CanonicalDir(absPath)
	if cand == "" {
		return CatalogAddress{}, false
	}
	checkoutRoot, matched := r.findViaTrustedWorktrees(cand, false)
	if len(matched) == 0 {
		return CatalogAddress{}, false
	}
	binding, ok := buildCheckoutBinding(checkoutRoot, checkoutRoot, matched)
	if !ok {
		return CatalogAddress{}, false
	}
	return binding.Project(cand)
}

func (r *CheckoutResolver) findWithinAdmitted(targetCanon, admittedCanon string) (string, []trustedAttachment, bool) {
	for cur := targetCanon; ; {
		dot := WorktreeEntry(cur)
		_, err := os.Lstat(dot)
		if err == nil {
			var matched []trustedAttachment
			for _, att := range r.attached {
				if verifyLinkedWorktree(cur, att) {
					matched = append(matched, att)
				}
			}
			return cur, matched, false
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, false
		}
		if cur == admittedCanon {
			return "", nil, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", nil, false
		}
		cur = parent
	}
}

func (r *CheckoutResolver) findViaTrustedWorktrees(targetCanon string, strictDescendantOnly bool) (string, []trustedAttachment) {
	type candidate struct {
		root string
		atts []trustedAttachment
	}
	byRoot := map[string]*candidate{}
	for _, att := range r.attached {
		wtRoot, ok := findContainingRegisteredWorktree(targetCanon, att, strictDescendantOnly)
		if !ok {
			continue
		}
		entry := byRoot[wtRoot]
		if entry == nil {
			entry = &candidate{root: wtRoot}
			byRoot[wtRoot] = entry
		}
		entry.atts = append(entry.atts, att)
	}
	var best *candidate
	ambiguous := false
	for _, entry := range byRoot {
		if best == nil {
			best = entry
			continue
		}
		switch {
		case isStrictDescendant(best.root, entry.root):
			best, ambiguous = entry, false
		case isStrictDescendant(entry.root, best.root):
		default:
			ambiguous = true
		}
	}
	if best == nil || ambiguous {
		return "", nil
	}
	return best.root, best.atts
}
