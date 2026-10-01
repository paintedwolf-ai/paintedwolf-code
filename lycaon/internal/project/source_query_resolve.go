package project

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

const sourceRemoteTimeout = 3 * time.Second

// remoteFileQuery maps a code host link onto the root checked out from its repository. The ref
// ends at the longest prefix the checkout knows as a ref, else where the rest names a path there;
// another repository's link assumes a one-segment ref and matches by path.
func (s SourceIndexSnapshot) remoteFileQuery(ctx context.Context, remote SourceRemoteFile, style SourcePathStyle) string {
	for _, root := range s.readers {
		refs, ok := checkoutRefs(ctx, root.path, remote.Repo)
		if !ok {
			continue
		}
		split := 0
		for k := len(remote.Segments) - 1; k >= 1 && split == 0; k-- {
			if ref := strings.Join(remote.Segments[:k], "/"); refs[ref] || sourceCommitID.MatchString(ref) {
				split = k
			}
		}
		for k := 1; k < len(remote.Segments) && split == 0; k++ {
			if _, err := os.Stat(filepath.Join(root.path, filepath.FromSlash(strings.Join(remote.Segments[k:], "/")))); err == nil {
				split = k
			}
		}
		if split > 0 {
			return style.normalize(root.path) + "/" + strings.ToLower(strings.Join(remote.Segments[split:], "/"))
		}
	}
	return "/" + strings.ToLower(strings.Join(remote.Segments[1:], "/"))
}

var sourceCommitID = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// checkoutRefs lists the short names of dir's branches, remote-tracking branches, and tags when
// one of its remotes is repo.
func checkoutRefs(ctx context.Context, dir, repo string) (map[string]bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, sourceRemoteTimeout)
	defer cancel()
	git := func(args ...string) []string {
		out, code, err := gitexec.Run(ctx, dir, args, gitexec.Opts{Profile: gitexec.ProfileHermetic, Timeout: sourceRemoteTimeout})
		if err != nil || code != 0 {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	matched := false
	remotes := map[string]bool{}
	for _, line := range git("remote", "-v") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			remotes[fields[0]] = true
			matched = matched || SourceRemoteRepo(fields[1]) == repo
		}
	}
	if !matched {
		return nil, false
	}
	refs := map[string]bool{}
	for _, name := range git("for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags", "refs/remotes") {
		for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
			short, ok := strings.CutPrefix(name, prefix)
			if !ok {
				continue
			}
			if prefix == "refs/remotes/" {
				if remote, branch, found := strings.Cut(short, "/"); found && remotes[remote] {
					short = branch
				}
			}
			refs[short] = true
		}
	}
	return refs, true
}

// SourceOutsidePath is an absolute query naming something on this host outside every root.
type SourceOutsidePath struct {
	Path      string
	Directory bool
}

// OutsidePath reports an absolute query that exists on this host but under no selected root.
func (s SourceIndexSnapshot) OutsidePath(query SourceQuery, style SourcePathStyle) (SourceOutsidePath, bool) {
	if query.Absolute == "" {
		return SourceOutsidePath{}, false
	}
	target := style.normalize(query.Absolute)
	for _, root := range s.rootPaths {
		spellings := []string{root}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			spellings = append(spellings, resolved)
		}
		for _, spelling := range spellings {
			base := strings.TrimRight(style.normalize(spelling), "/")
			if target == base || strings.HasPrefix(target, base+"/") {
				return SourceOutsidePath{}, false
			}
		}
	}
	info, err := os.Stat(filepath.FromSlash(query.Absolute))
	if err != nil {
		return SourceOutsidePath{}, false
	}
	return SourceOutsidePath{Path: filepath.Clean(filepath.FromSlash(query.Absolute)), Directory: info.IsDir()}, true
}
