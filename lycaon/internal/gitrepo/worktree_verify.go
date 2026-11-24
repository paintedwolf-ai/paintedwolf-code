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

func findContainingRegisteredWorktree(targetCanon string, att trustedAttachment, strictDescendantOnly bool) (string, bool) {
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
		if wtRoot == "" {
			continue
		}
		if strictDescendantOnly {
			if !isStrictDescendant(wtRoot, targetCanon) {
				continue
			}
		} else if _, within := relWithin(wtRoot, targetCanon); !within {
			continue
		}
		if !verifyLinkedWorktree(wtRoot, att) || hasIntermediateGitBoundary(wtRoot, targetCanon) {
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
	if ancestor == descendant {
		return false
	}
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return true
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := ancestor
	for i := 0; i < len(parts)-1; i++ {
		cur = filepath.Join(cur, parts[i])
		if _, err := os.Lstat(WorktreeEntry(cur)); err == nil {
			return true
		} else if !errors.Is(err, fs.ErrNotExist) {
			if _, dirErr := os.Lstat(cur); dirErr == nil {
				return true
			}
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
