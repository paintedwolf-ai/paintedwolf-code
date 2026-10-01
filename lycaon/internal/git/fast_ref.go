package git

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitrepo"
)

// ReadHeadSHA resolves HEAD to a 40-char (or abbreviated) object name via
// direct.git format reads — no subprocess.
func ReadHeadSHA(projectDir string) (string, error) {
	repo, err := discoverRepo(projectDir)
	if err != nil {
		return "", err
	}
	head, err := readHead(repo)
	if err != nil {
		return "", err
	}
	if ref, ok := headSymref(head); ok {
		return resolveRef(repo, ref)
	}
	if isObjectName(head) {
		return head, nil
	}
	return "", fmt.Errorf("unrecognized HEAD: %q", head)
}

// ReadBranch returns the current branch name from HEAD, or "" when detached.
func ReadBranch(projectDir string) (string, error) {
	repo, err := discoverRepo(projectDir)
	if err != nil {
		return "", err
	}
	head, err := readHead(repo)
	if err != nil {
		return "", err
	}
	// A detached HEAD, or a symref outside refs/heads, has no branch name.
	if ref, ok := headSymref(head); ok && strings.HasPrefix(ref, branchRefPrefix) {
		return strings.TrimPrefix(ref, branchRefPrefix), nil
	}
	return "", nil
}

// branchRefPrefix is where a checked-out branch lives.
const branchRefPrefix = "refs/heads/"

// readHead returns the trimmed contents of HEAD, which is per-worktree state: the shared
// HEAD names whatever the main worktree has checked out.
func readHead(repo gitrepo.Repo) (string, error) {
	raw, err := os.ReadFile(filepath.Join(repo.GitDir, "HEAD"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// headSymref reports the ref HEAD points at, and false when HEAD is detached.
func headSymref(head string) (string, bool) {
	const prefix = "ref: "
	if !strings.HasPrefix(head, prefix) {
		return "", false
	}
	ref := strings.TrimSpace(strings.TrimPrefix(head, prefix))
	if ref == "" {
		return "", false
	}
	return ref, true
}

// AheadBehind is the divergence of the current branch from its upstream tip.
type AheadBehind struct {
	Ahead  int
	Behind int
	// Upstream is remotes/<remote>/<branch> path when configured; empty if none.
	Upstream string
	// Exact marks counts resolved without a commit walk.
	Exact bool
}

// ReadAheadBehind resolves equal tips directly and leaves divergent counts inexact.
func ReadAheadBehind(projectDir string) (AheadBehind, error) {
	repo, err := discoverRepo(projectDir)
	if err != nil {
		return AheadBehind{}, err
	}
	branch, err := ReadBranch(projectDir)
	if err != nil {
		return AheadBehind{}, err
	}
	if branch == "" {
		return AheadBehind{Exact: true}, nil
	}
	upstream, err := readBranchUpstream(repo, branch)
	if err != nil {
		// Configuration errors defer counting to Git.
		return AheadBehind{}, err
	}
	if !upstream.configured {
		// A branch without an upstream has no divergence.
		return AheadBehind{Exact: true}, nil
	}
	localSHA, err := resolveRef(repo, branchRefPrefix+branch)
	if err != nil {
		return AheadBehind{}, err
	}
	// The merge ref names a branch; its fetched tip is a remote-tracking ref.
	short := strings.TrimPrefix(upstream.merge, branchRefPrefix)
	upstreamRef := "refs/remotes/" + upstream.remote + "/" + short
	upSHA, err := resolveRef(repo, upstreamRef)
	if err != nil {
		// An unfetched upstream has no tip to compare.
		return AheadBehind{Upstream: upstreamRef, Exact: true}, nil //nolint:nilerr // no upstream tip to compare against
	}
	out := AheadBehind{Upstream: upstreamRef}
	if localSHA == upSHA {
		out.Exact = true
		return out, nil
	}
	// Divergent tips require a commit walk.
	return out, nil
}

// discoverRepo searches ancestors and separates worktree state from shared state.
func discoverRepo(projectDir string) (gitrepo.Repo, error) {
	repo, ok := gitrepo.Discover(projectDir)
	if !ok {
		return gitrepo.Repo{}, fmt.Errorf("%w: %s", ErrNotRepository, projectDir)
	}
	if repo.GitDir == "" || repo.CommonDir == "" {
		return gitrepo.Repo{}, fmt.Errorf("unresolved git directory layout for repository %s", repo.Root)
	}
	return repo, nil
}

// perWorktreeRefPrefixes names refs stored outside the common directory.
var perWorktreeRefPrefixes = []string{"refs/bisect/", "refs/worktree/", "refs/rewritten/"}

func isPerWorktreeRef(ref string) bool {
	for _, prefix := range perWorktreeRefPrefixes {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}

// resolveRef reads a ref to an object name, following symrefs.
//
// Storage is decided per hop because a per-worktree HEAD may point at a shared branch.
func resolveRef(repo gitrepo.Repo, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty ref")
	}
	perWorktree := isPerWorktreeRef(ref)
	home := repo.CommonDir
	if perWorktree {
		home = repo.GitDir
	}
	loose, err := refPathUnder(home, filepath.FromSlash(ref))
	if err != nil {
		return "", err
	}
	if raw, err := os.ReadFile(loose); err == nil {
		val := strings.TrimSpace(string(raw))
		if target, ok := headSymref(val); ok {
			return resolveRef(repo, target)
		}
		if isObjectName(val) {
			return val, nil
		}
	}
	if perWorktree {
		return "", fmt.Errorf("ref not found: %s", ref)
	}
	return resolvePackedRef(repo.CommonDir, ref)
}

// refPathUnder joins rel under dir and rejects path escape (.. / absolute).
func refPathUnder(dir, rel string) (string, error) {
	dir = filepath.Clean(dir)
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || cleanRel == "" || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("invalid ref path %q", rel)
	}
	joined := filepath.Join(dir, cleanRel)
	if joined != dir && !strings.HasPrefix(joined, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("ref escapes git dir: %q", rel)
	}
	return joined, nil
}

// resolvePackedRef reads the shared packed-refs file, which only the common directory has.
func resolvePackedRef(commonDir, ref string) (string, error) {
	f, err := os.Open(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		if parts[1] == ref && isObjectName(parts[0]) {
			return parts[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("ref not found: %s", ref)
}

// branchUpstream is the upstream one branch names in config.
type branchUpstream struct {
	remote string
	merge  string
	// configured is true only when the branch names both halves. Kept distinct from a read
	// failure, which is an error: "no upstream" is an answer, "cannot tell" is not.
	configured bool
}

// readBranchUpstream reads remote and merge keys from shared config.
// Worktree overrides require full config resolution.
func readBranchUpstream(repo gitrepo.Repo, branch string) (branchUpstream, error) {
	if _, err := os.Stat(filepath.Join(repo.GitDir, "config.worktree")); err == nil {
		return branchUpstream{}, fmt.Errorf("per-worktree config may override branch settings")
	}
	raw, err := os.ReadFile(filepath.Join(repo.CommonDir, "config"))
	if err != nil {
		return branchUpstream{}, err
	}
	var remote, merge string
	section := []byte("[branch \"" + branch + "\"]")
	idx := bytes.Index(raw, section)
	if idx < 0 {
		return branchUpstream{}, nil
	}
	rest := raw[idx+len(section):]
	if next := bytes.IndexByte(rest, '['); next >= 0 {
		rest = rest[:next]
	}
	sc := bufio.NewScanner(bytes.NewReader(rest))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "remote":
			remote = val
		case "merge":
			merge = val
		}
	}
	if err := sc.Err(); err != nil {
		return branchUpstream{}, err
	}
	return branchUpstream{remote: remote, merge: merge, configured: remote != "" && merge != ""}, nil
}

func isObjectName(s string) bool {
	if n := len(s); n < 7 || n > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
