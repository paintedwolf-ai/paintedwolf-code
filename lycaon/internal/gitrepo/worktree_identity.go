package gitrepo

import (
	"bytes"
	"errors"
	"io"
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

// CheckoutResolver matches admitted external directories against attached repositories.
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
		att, ok := anchorAttachment(id, root.Path)
		if ok {
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
	checkoutRoot, matched = r.findViaTrustedWorktrees(admittedCanon)
	if len(matched) == 0 {
		return CheckoutBinding{}, false
	}
	return buildCheckoutBinding(checkoutRoot, admittedCanon, matched)
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

func (r *CheckoutResolver) findViaTrustedWorktrees(admittedCanon string) (string, []trustedAttachment) {
	type candidate struct {
		root string
		atts []trustedAttachment
	}
	byRoot := map[string]*candidate{}
	for _, att := range r.attached {
		wtRoot, ok := findContainingRegisteredWorktree(admittedCanon, att)
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

func findContainingRegisteredWorktree(admittedCanon string, att trustedAttachment) (string, bool) {
	entries, err := os.ReadDir(att.worktrees)
	if err != nil || len(entries) > maxWorktreeAdmins {
		return "", false
	}
	var best string
	for _, d := range entries {
		if d.Type()&fs.ModeSymlink != 0 || !d.IsDir() {
			continue
		}
		adminDir := filepath.Join(att.worktrees, d.Name())
		rawReverse, ok := readBoundedPointer(filepath.Join(adminDir, "gitdir"), "")
		if !ok {
			continue
		}
		reverseDotGit := resolveExactAgainst(adminDir, rawReverse)
		if filepath.Base(reverseDotGit) != dotGit {
			continue
		}
		wtRoot := CanonicalDir(filepath.Dir(reverseDotGit))
		if wtRoot == "" || !isStrictDescendant(wtRoot, admittedCanon) {
			continue
		}
		if !verifyLinkedWorktree(wtRoot, att) || hasIntermediateGitBoundary(wtRoot, admittedCanon) {
			continue
		}
		if best == "" || isStrictDescendant(best, wtRoot) {
			best = wtRoot
		} else if best != wtRoot && !isStrictDescendant(wtRoot, best) {
			return "", false
		}
	}
	return best, best != ""
}

func hasIntermediateGitBoundary(ancestor, descendant string) bool {
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return true
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := ancestor
	for i := 0; i < len(parts)-1; i++ {
		cur = filepath.Join(cur, parts[i])
		if _, err := os.Lstat(WorktreeEntry(cur)); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

func buildCheckoutBinding(checkoutRoot, admittedCanon string, matched []trustedAttachment) (CheckoutBinding, bool) {
	var firstCommon os.FileInfo
	for _, att := range matched {
		if firstCommon == nil {
			firstCommon = att.commonID
			continue
		}
		if !os.SameFile(firstCommon, att.commonID) {
			return CheckoutBinding{}, false
		}
	}
	var bases []CatalogBase
	for _, att := range matched {
		mapped := checkoutRoot
		if att.repoRel != "." {
			mapped = filepath.Join(checkoutRoot, filepath.FromSlash(att.repoRel))
		}
		basePath, ok := intersectDirs(mapped, admittedCanon)
		if !ok {
			continue
		}
		info, err := os.Stat(basePath)
		if err != nil || !info.IsDir() {
			continue
		}
		id, err := fspath.EntryIdentity(basePath + string(filepath.Separator) + ".")
		if err != nil {
			continue
		}
		bases = append(bases, CatalogBase{
			RootID:       att.rootID,
			CheckoutRoot: checkoutRoot,
			BasePath:     basePath,
			BaseIdentity: id,
		})
	}
	if len(bases) == 0 {
		return CheckoutBinding{}, false
	}
	return CheckoutBinding{CheckoutRoot: checkoutRoot, Bases: bases}, true
}

func anchorAttachment(rootID, rootPath string) (trustedAttachment, bool) {
	rootCanon := CanonicalDir(rootPath)
	if rootCanon == "" {
		return trustedAttachment{}, false
	}
	if info, err := os.Stat(rootCanon); err != nil || !info.IsDir() {
		return trustedAttachment{}, false
	}
	repoRoot := FindRoot(rootCanon)
	if repoRoot == "" {
		return trustedAttachment{}, false
	}
	gitDir, commonDir, ok := resolveTrustedRepoLayout(repoRoot)
	if !ok || gitDir == "" || commonDir == "" {
		return trustedAttachment{}, false
	}
	commonCanon := CanonicalDir(commonDir)
	commonInfo, err := os.Stat(commonCanon)
	if err != nil || !commonInfo.IsDir() {
		return trustedAttachment{}, false
	}
	repoRel, ok := relWithin(repoRoot, rootCanon)
	if !ok {
		return trustedAttachment{}, false
	}
	return trustedAttachment{
		rootID:    rootID,
		repoRoot:  repoRoot,
		repoRel:   repoRel,
		commonDir: commonCanon,
		commonID:  commonInfo,
		worktrees: filepath.Join(commonCanon, "worktrees"),
	}, true
}

func resolveTrustedRepoLayout(repoRoot string) (gitDir, commonDir string, ok bool) {
	dot := WorktreeEntry(repoRoot)
	info, err := os.Lstat(dot)
	if err != nil {
		return "", "", false
	}
	switch {
	case info.IsDir():
		return dot, dot, true
	case info.Mode().IsRegular():
		rawGitDir, valid := readBoundedPointer(dot, gitDirLinePrefix)
		if !valid {
			return "", "", false
		}
		gitDir = resolveExactAgainst(repoRoot, rawGitDir)
		info, err := os.Stat(gitDir)
		if err != nil || !info.IsDir() {
			return "", "", false
		}
		commonPath := filepath.Join(gitDir, "commondir")
		if _, lstatErr := os.Lstat(commonPath); errors.Is(lstatErr, fs.ErrNotExist) {
			return gitDir, gitDir, true
		}
		rawCommon, valid := readBoundedPointer(commonPath, "")
		if !valid {
			return "", "", false
		}
		commonDir = resolveExactAgainst(gitDir, rawCommon)
		return gitDir, commonDir, commonDir != ""
	default:
		return "", "", false
	}
}

func verifyLinkedWorktree(candCanon string, att trustedAttachment) bool {
	candInfo, err := os.Stat(candCanon)
	if err != nil || !candInfo.IsDir() {
		return false
	}
	dotPath := WorktreeEntry(candCanon)
	dotInfo, err := os.Lstat(dotPath)
	if err != nil || !dotInfo.Mode().IsRegular() {
		return false
	}
	rawAdmin, ok := readBoundedPointer(dotPath, gitDirLinePrefix)
	if !ok {
		return false
	}
	adminTarget := resolveExactAgainst(candCanon, rawAdmin)
	if adminTarget == "" {
		return false
	}
	worktreesInfo, err := os.Stat(att.worktrees)
	if err != nil || !worktreesInfo.IsDir() {
		return false
	}
	adminParent := filepath.Dir(adminTarget)
	adminName := filepath.Base(adminTarget)
	if adminName == "" || adminName == "." || adminName == ".." || !sameFileStat(adminParent, worktreesInfo) {
		return false
	}
	adminEntry := filepath.Join(att.worktrees, adminName)
	adminEntryInfo, err := os.Lstat(adminEntry)
	if err != nil || !adminEntryInfo.IsDir() {
		return false
	}
	rawCommon, ok := readBoundedPointer(filepath.Join(adminEntry, "commondir"), "")
	if !ok {
		return false
	}
	commonTarget := resolveExactAgainst(adminEntry, rawCommon)
	if !sameFileStat(commonTarget, att.commonID) {
		return false
	}
	rawReverse, ok := readBoundedPointer(filepath.Join(adminEntry, "gitdir"), "")
	if !ok {
		return false
	}
	reverseTarget := resolveExactAgainst(adminEntry, rawReverse)
	if filepath.Base(reverseTarget) != dotGit {
		return false
	}
	reverseInfo, err := os.Lstat(reverseTarget)
	if err != nil || !reverseInfo.Mode().IsRegular() || !os.SameFile(reverseInfo, dotInfo) {
		return false
	}
	return sameFileStat(filepath.Dir(reverseTarget), candInfo)
}

func readBoundedPointer(path, prefix string) (string, bool) {
	linfo, err := os.Lstat(path)
	if err != nil || !linfo.Mode().IsRegular() || linfo.Size() <= 0 || linfo.Size() > maxPointerBytes {
		return "", false
	}
	// #nosec G304 -- caller validates containment against admitted or trusted repository paths.
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	finfo, err := f.Stat()
	if err != nil || !finfo.Mode().IsRegular() || !os.SameFile(linfo, finfo) {
		return "", false
	}
	var buf [maxPointerBytes + 1]byte
	n, err := io.ReadFull(f, buf[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false
	}
	if n == 0 || n > maxPointerBytes || bytes.IndexByte(buf[:n], 0) >= 0 {
		return "", false
	}
	line := string(buf[:n])
	line, hasNewline := strings.CutSuffix(line, "\n")
	if hasNewline {
		line = strings.TrimSuffix(line, "\r")
	}
	if strings.ContainsRune(line, '\n') {
		return "", false
	}
	if prefix != "" {
		var matched bool
		line, matched = strings.CutPrefix(line, prefix)
		if !matched {
			return "", false
		}
	}
	if line == "" {
		return "", false
	}
	return line, true
}

func resolveExactAgainst(base, value string) string {
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}

func sameFileStat(path string, expected os.FileInfo) bool {
	if path == "" || expected == nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && os.SameFile(info, expected)
}

func relWithin(base, target string) (string, bool) {
	if base == target {
		return ".", true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isStrictDescendant(ancestor, target string) bool {
	rel, ok := relWithin(ancestor, target)
	return ok && rel != "."
}

func intersectDirs(a, b string) (string, bool) {
	if _, ok := relWithin(a, b); ok {
		return b, true
	}
	if _, ok := relWithin(b, a); ok {
		return a, true
	}
	return "", false
}
