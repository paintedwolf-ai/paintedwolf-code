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
	add := func(url string) {
		if url = strings.TrimSpace(url); url != "" && !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	for _, target := range targets {
		if !slices.Contains(configured, target) {
			if strings.HasPrefix(target, "-") || strings.ContainsAny(target, "\x00\n") {
				return nil, fmt.Errorf("%w: %q", ErrRemoteUnresolved, target)
			}
			add(rewriteURL(ctx, repoDir, target, intent.Push))
			continue
		}
		args := []string{"remote", "get-url", "--all"}
		if intent.Push {
			args = append(args, "--push")
		}
		out, ok := runRemoteQuery(ctx, repoDir, append(args, target))
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrRemoteUnresolved, target)
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			add(line)
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

// rewriteURL applies the repository's url.<base>.insteadOf rules to a URL
// operand, and for push its pushInsteadOf rules first, each by the longest
// matching prefix as Git does. Git names only repository-configured remotes,
// so a URL operand cannot be resolved by asking it.
func rewriteURL(ctx context.Context, repoDir, raw string, push bool) string {
	out, ok := runRemoteQuery(ctx, repoDir, []string{"config", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`})
	if !ok {
		// No rule is configured; Git exits non-zero for an empty match.
		return raw
	}
	type rule struct{ base, prefix string }
	var fetchRules, pushRules []rule
	for line := range strings.SplitSeq(string(out), "\n") {
		key, prefix, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		lower := strings.ToLower(key)
		switch {
		case strings.HasSuffix(lower, ".pushinsteadof"):
			pushRules = append(pushRules, rule{base: key[len("url.") : len(key)-len(".pushinsteadof")], prefix: prefix})
		case strings.HasSuffix(lower, ".insteadof"):
			fetchRules = append(fetchRules, rule{base: key[len("url.") : len(key)-len(".insteadof")], prefix: prefix})
		}
	}
	longest := func(rules []rule) (string, bool) {
		best := -1
		for i, r := range rules {
			if strings.HasPrefix(raw, r.prefix) && (best < 0 || len(r.prefix) > len(rules[best].prefix)) {
				best = i
			}
		}
		if best < 0 {
			return "", false
		}
		return rules[best].base + raw[len(rules[best].prefix):], true
	}
	if push {
		if rewritten, ok := longest(pushRules); ok {
			return rewritten
		}
	}
	if rewritten, ok := longest(fetchRules); ok {
		return rewritten
	}
	return raw
}
