package gitexec

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrRemoteUnresolved refuses a network command whose recipients Git cannot
// name; an unknown target never becomes an unrestricted one.
var ErrRemoteUnresolved = errors.New("gitexec: the command's remote does not resolve to a URL")

// RemoteIntent is what a network command names, read from its parsed operands.
type RemoteIntent struct {
	// Push selects push URLs and push defaults.
	Push bool
	// Remote is a configured remote name or a URL; empty selects the default.
	Remote string
	// All selects every configured remote (fetch --all).
	All bool
}

// transientRemote names a remote defined only for one resolution query, so a
// URL operand receives the same rewriting as a configured remote.
const transientRemote = "pw-resolve"

// ResolveRemoteURLs returns every URL the command contacts, after the
// repository's url.*.insteadOf and pushInsteadOf rewrites, as Git itself
// resolves them: fetch versus push defaults, branch.*.pushRemote,
// remote.pushDefault and every pushurl.
func ResolveRemoteURLs(ctx context.Context, repoDir string, intent RemoteIntent) ([]string, error) {
	configured, err := remoteNames(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	var targets []string
	switch {
	case intent.All:
		targets = configured
	case intent.Remote != "":
		targets = []string{intent.Remote}
	default:
		targets = []string{defaultRemote(ctx, repoDir, intent.Push)}
	}
	var urls []string
	for _, target := range targets {
		args := []string{"remote", "get-url", "--all"}
		if intent.Push {
			args = append(args, "--push")
		}
		if slices.Contains(configured, target) {
			args = append(args, target)
		} else {
			if strings.HasPrefix(target, "-") || strings.ContainsAny(target, "\x00\n") {
				return nil, fmt.Errorf("%w: %q", ErrRemoteUnresolved, target)
			}
			args = append([]string{"-c", "remote." + transientRemote + ".url=" + target}, append(args, transientRemote)...)
		}
		out, ok := runRemoteQuery(ctx, repoDir, args)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrRemoteUnresolved, target)
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" && !slices.Contains(urls, line) {
				urls = append(urls, line)
			}
		}
	}
	if len(urls) == 0 {
		return nil, ErrRemoteUnresolved
	}
	return urls, nil
}

func remoteNames(ctx context.Context, repoDir string) ([]string, error) {
	out, ok := runRemoteQuery(ctx, repoDir, []string{"remote"})
	if !ok {
		return nil, errors.New("gitexec: list remotes")
	}
	return strings.Fields(string(out)), nil
}

// defaultRemote follows Git's selection: push prefers the branch's
// pushRemote, then remote.pushDefault; both fall back to the branch's remote
// and then origin.
func defaultRemote(ctx context.Context, repoDir string, push bool) string {
	branch := ""
	if out, ok := runRemoteQuery(ctx, repoDir, []string{"symbolic-ref", "--short", "-q", "HEAD"}); ok {
		branch = firstNonEmptyLine(out)
	}
	var keys []string
	if push && branch != "" {
		keys = append(keys, "branch."+branch+".pushRemote")
	}
	if push {
		keys = append(keys, "remote.pushDefault")
	}
	if branch != "" {
		keys = append(keys, "branch."+branch+".remote")
	}
	for _, key := range keys {
		if out, ok := runRemoteQuery(ctx, repoDir, []string{"config", "--get", key}); ok {
			if name := firstNonEmptyLine(out); name != "" {
				return name
			}
		}
	}
	return defaultRemoteName
}
