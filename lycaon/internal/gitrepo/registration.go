package gitrepo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// CheckoutKind distinguishes the repository's primary working tree from linked worktrees.
type CheckoutKind string

const (
	CheckoutPrimary CheckoutKind = "primary"
	CheckoutLinked  CheckoutKind = "linked"
)

// RegisteredCheckout records a verified checkout registration and its filesystem identities.
type RegisteredCheckout struct {
	Kind             CheckoutKind
	Root             string
	AdminDir         string
	CommonDir        string
	Available        bool
	CommonIdentity   string
	AdminIdentity    string
	WorktreeIdentity string
}

// CommonRepository resolves the canonical shared Git directory for a working tree or bare repository.
func CommonRepository(dir string) (commonDir, primaryRoot string, bare, ok bool) {
	if strings.TrimSpace(dir) == "" {
		return "", "", false, false
	}
	canon := CanonicalDir(dir)
	if canon == "" {
		return "", "", false, false
	}
	if repo, found := Discover(canon); found && repo.CommonDir != "" {
		commonCanon := CanonicalDir(repo.CommonDir)
		if commonCanon == "" {
			return "", "", false, false
		}
		if info, err := os.Stat(commonCanon); err != nil || !info.IsDir() {
			return "", "", false, false
		}
		if isMarkedBare(commonCanon) {
			return commonCanon, "", true, true
		}
		primary := primaryWorktreeForCommonDir(commonCanon)
		return commonCanon, primary, false, true
	}
	if isBareRepository(canon) {
		return canon, "", true, true
	}
	return "", "", false, false
}

// VerifyRegisteredCheckout verifies forward and reverse Git metadata between commonDir and candidateDir.
func VerifyRegisteredCheckout(commonDir, candidateDir string) (RegisteredCheckout, bool) {
	commonCanon := CanonicalDir(commonDir)
	candidateCanon := CanonicalDir(candidateDir)
	if commonCanon == "" || candidateCanon == "" {
		return RegisteredCheckout{}, false
	}
	commonInfo, err := os.Stat(commonCanon)
	if err != nil || !commonInfo.IsDir() {
		return RegisteredCheckout{}, false
	}
	candidateInfo, err := os.Stat(candidateCanon)
	if err != nil || !candidateInfo.IsDir() {
		return RegisteredCheckout{}, false
	}
	if os.SameFile(commonInfo, candidateInfo) {
		return RegisteredCheckout{}, false
	}

	dot := WorktreeEntry(candidateCanon)
	dotLstat, err := os.Lstat(dot)
	if err != nil {
		return RegisteredCheckout{}, false
	}

	if dotInfo, statErr := os.Stat(dot); statErr == nil && dotInfo.IsDir() {
		if !os.SameFile(dotInfo, commonInfo) || isMarkedBare(commonCanon) {
			return RegisteredCheckout{}, false
		}
		resolvedCommon := readCommonDir(commonCanon)
		resolvedCommonInfo, commonErr := os.Stat(resolvedCommon)
		if commonErr != nil || !os.SameFile(resolvedCommonInfo, commonInfo) {
			return RegisteredCheckout{}, false
		}
		if configured := configuredWorktree(commonCanon); configured != "" {
			cfgInfo, cfgErr := os.Stat(configured)
			if cfgErr != nil || !os.SameFile(cfgInfo, candidateInfo) {
				return RegisteredCheckout{}, false
			}
		}
		id := entryIdentity(commonCanon)
		return RegisteredCheckout{
			Kind:             CheckoutPrimary,
			Root:             candidateCanon,
			AdminDir:         commonCanon,
			CommonDir:        commonCanon,
			Available:        true,
			CommonIdentity:   id,
			AdminIdentity:    id,
			WorktreeIdentity: entryIdentity(candidateCanon),
		}, true
	}

	if !dotLstat.Mode().IsRegular() {
		return RegisteredCheckout{}, false
	}
	dotLine, exists, valid := readOptionalSingleLine(dot)
	if !exists || !valid {
		return RegisteredCheckout{}, false
	}
	rawAdmin := GitDirFromPointer(candidateCanon, dotLine)
	if rawAdmin == "" {
		return RegisteredCheckout{}, false
	}
	adminCanon := CanonicalDir(rawAdmin)
	if adminCanon == "" {
		return RegisteredCheckout{}, false
	}
	adminInfo, err := os.Stat(adminCanon)
	if err != nil || !adminInfo.IsDir() {
		return RegisteredCheckout{}, false
	}
	worktreesParentInfo, err := os.Stat(filepath.Join(commonCanon, "worktrees"))
	if err != nil || !worktreesParentInfo.IsDir() {
		return RegisteredCheckout{}, false
	}
	adminParentInfo, err := os.Stat(filepath.Dir(adminCanon))
	if err != nil || !os.SameFile(worktreesParentInfo, adminParentInfo) || os.SameFile(adminInfo, worktreesParentInfo) {
		return RegisteredCheckout{}, false
	}

	commondirFile := filepath.Join(adminCanon, "commondir")
	if info, lstatErr := os.Lstat(commondirFile); lstatErr != nil || !info.Mode().IsRegular() {
		return RegisteredCheckout{}, false
	}
	resolvedCommon := readCommonDir(adminCanon)
	resolvedCommonInfo, err := os.Stat(resolvedCommon)
	if err != nil || !os.SameFile(resolvedCommonInfo, commonInfo) {
		return RegisteredCheckout{}, false
	}

	reverseLine, revExists, revValid := readOptionalSingleLine(filepath.Join(adminCanon, "gitdir"))
	if !revExists || !revValid {
		return RegisteredCheckout{}, false
	}
	reverseDot := resolveAgainst(adminCanon, reverseLine)
	if filepath.Base(reverseDot) != dotGit {
		return RegisteredCheckout{}, false
	}
	reverseDotLstat, err := os.Lstat(reverseDot)
	if err != nil || !reverseDotLstat.Mode().IsRegular() || !os.SameFile(dotLstat, reverseDotLstat) {
		return RegisteredCheckout{}, false
	}
	reverseRootInfo, err := os.Stat(filepath.Dir(reverseDot))
	if err != nil || !os.SameFile(reverseRootInfo, candidateInfo) {
		return RegisteredCheckout{}, false
	}

	return RegisteredCheckout{
		Kind:             CheckoutLinked,
		Root:             candidateCanon,
		AdminDir:         adminCanon,
		CommonDir:        commonCanon,
		Available:        true,
		CommonIdentity:   entryIdentity(commonCanon),
		AdminIdentity:    entryIdentity(adminCanon),
		WorktreeIdentity: entryIdentity(candidateCanon),
	}, true
}

// InspectListedCheckout classifies an entry reported by `git worktree list --porcelain -z`.
// Unavailable entries still retain their administrative directory identity when uniquely matched.
func InspectListedCheckout(commonDir, listedPath string) RegisteredCheckout {
	if verified, ok := VerifyRegisteredCheckout(commonDir, listedPath); ok {
		return verified
	}
	commonCanon := CanonicalDir(commonDir)
	rootCanon := canonicalOrClean(listedPath)
	out := RegisteredCheckout{
		Kind:           CheckoutLinked,
		Root:           rootCanon,
		CommonDir:      commonCanon,
		Available:      false,
		CommonIdentity: entryIdentity(commonCanon),
	}
	if commonCanon == "" || rootCanon == "" {
		return out
	}
	if primary := primaryWorktreeForCommonDir(commonCanon); primary != "" && canonicalOrClean(primary) == rootCanon {
		out.Kind = CheckoutPrimary
		out.AdminDir = commonCanon
		out.AdminIdentity = out.CommonIdentity
		return out
	}
	if adminCanon, ok := matchAdminDirByReversePointer(commonCanon, listedPath); ok {
		out.AdminDir = adminCanon
		out.AdminIdentity = entryIdentity(adminCanon)
	}
	return out
}

// ProjectSubtree maps attachedRootPath from sourceToplevel onto targetCheckoutRoot,
// refusing nested repository boundaries inside the projected path.
func ProjectSubtree(sourceToplevel, targetCheckoutRoot, attachedRootPath string) (string, bool) {
	srcTop := canonicalOrClean(sourceToplevel)
	rootCanon := canonicalOrClean(attachedRootPath)
	if srcTop == "" || rootCanon == "" || strings.TrimSpace(targetCheckoutRoot) == "" {
		return "", false
	}
	dstClean, err := filepath.Abs(targetCheckoutRoot)
	if err != nil {
		return "", false
	}
	dstClean = filepath.Clean(dstClean)
	rel, err := filepath.Rel(srcTop, rootCanon)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return dstClean, true
	}
	cur := dstClean
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if IsRoot(cur) {
			return "", false
		}
	}
	return cur, true
}

func matchAdminDirByReversePointer(commonCanon, listedPath string) (string, bool) {
	commonInfo, err := os.Stat(commonCanon)
	if err != nil || !commonInfo.IsDir() {
		return "", false
	}
	entries, err := os.ReadDir(filepath.Join(commonCanon, "worktrees"))
	if err != nil {
		return "", false
	}
	wantRoot := canonicalOrClean(listedPath)
	var matched string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		adminDir := CanonicalDir(filepath.Join(commonCanon, "worktrees", entry.Name()))
		if adminDir == "" {
			continue
		}
		commondirFile := filepath.Join(adminDir, "commondir")
		if info, lstatErr := os.Lstat(commondirFile); lstatErr != nil || !info.Mode().IsRegular() {
			continue
		}
		resolvedCommon := readCommonDir(adminDir)
		resolvedInfo, statErr := os.Stat(resolvedCommon)
		if statErr != nil || !os.SameFile(resolvedInfo, commonInfo) {
			continue
		}
		line, exists, valid := readOptionalSingleLine(filepath.Join(adminDir, "gitdir"))
		if !exists || !valid {
			continue
		}
		reverseDot := resolveAgainst(adminDir, line)
		if filepath.Base(reverseDot) != dotGit {
			continue
		}
		if canonicalOrClean(filepath.Dir(reverseDot)) != wantRoot {
			continue
		}
		if matched != "" && matched != adminDir {
			return "", false
		}
		matched = adminDir
	}
	return matched, matched != ""
}

func primaryWorktreeForCommonDir(commonCanon string) string {
	if isMarkedBare(commonCanon) {
		return ""
	}
	if configured := configuredWorktree(commonCanon); configured != "" {
		return CanonicalDir(configured)
	}
	if filepath.Base(commonCanon) == dotGit {
		return CanonicalDir(filepath.Dir(commonCanon))
	}
	return ""
}

func isMarkedBare(commonCanon string) bool {
	val, ok := readCoreConfigValue(filepath.Join(commonCanon, "config"), "bare")
	return ok && strings.EqualFold(strings.TrimSpace(val), "true")
}

func configuredWorktree(commonCanon string) string {
	val, ok := readCoreConfigValue(filepath.Join(commonCanon, "config"), "worktree")
	if !ok || strings.TrimSpace(val) == "" {
		return ""
	}
	return resolveAgainst(commonCanon, val)
}

func readCoreConfigValue(configPath, key string) (string, bool) {
	// #nosec G304 -- configPath is fixed at <commonDir>/config.
	f, err := os.Open(configPath)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	inCore := false
	var last string
	found := false
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			inCore = strings.EqualFold(section, "core")
			continue
		}
		if !inCore {
			continue
		}
		k, v, hasEq := strings.Cut(trimmed, "=")
		if !strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		if !hasEq {
			last = "true"
			found = true
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		last = v
		found = true
	}
	return last, found
}

func entryIdentity(path string) string {
	if path == "" {
		return ""
	}
	id, err := fspath.EntryIdentity(path)
	if err != nil {
		return ""
	}
	return id
}

func canonicalOrClean(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if canon := fspath.CanonicalPath(abs); canon != "" {
		return filepath.FromSlash(canon)
	}
	return abs
}

// ContainingRegisteredCheckout resolves the nearest enclosing Git working tree
// for path and verifies its forward and reverse registration metadata.
func ContainingRegisteredCheckout(path string) (RegisteredCheckout, bool) {
	dir := existingAncestorDir(path)
	if dir == "" {
		return RegisteredCheckout{}, false
	}
	repo, ok := Discover(dir)
	if !ok || repo.Root == "" || repo.CommonDir == "" {
		return RegisteredCheckout{}, false
	}
	commonCanon := CanonicalDir(repo.CommonDir)
	if commonCanon == "" || isMarkedBare(commonCanon) {
		return RegisteredCheckout{}, false
	}
	return VerifyRegisteredCheckout(commonCanon, repo.Root)
}

// ReadCheckoutHead reads branch and detached HEAD state directly from adminDir/HEAD.
func ReadCheckoutHead(commonDir, adminDir string) (branch, headSHA string, detached bool) {
	line, exists, valid := readOptionalSingleLine(filepath.Join(adminDir, "HEAD"))
	if !exists || !valid || line == "" {
		return "", "", false
	}
	if strings.HasPrefix(line, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
		branch = strings.TrimPrefix(ref, "refs/heads/")
		if commonDir != "" && ref != "" {
			if sha, refExists, refValid := readOptionalSingleLine(filepath.Join(commonDir, filepath.FromSlash(ref))); refExists && refValid && isHexHash(sha) {
				headSHA = sha
			}
		}
		return branch, headSHA, false
	}
	if isHexHash(line) {
		return "", line, true
	}
	return "", "", false
}

func existingAncestorDir(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return ""
	}
	cur := filepath.Clean(abs)
	for {
		if info, statErr := os.Stat(cur); statErr == nil {
			if info.IsDir() {
				return CanonicalDir(cur)
			}
			parent := filepath.Dir(cur)
			if parent != "" {
				return CanonicalDir(parent)
			}
			return ""
		}
		next := filepath.Dir(cur)
		if next == cur || next == "" {
			return ""
		}
		cur = next
	}
}

func isHexHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}
