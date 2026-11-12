package gitcmd

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/evidence"
)

// networkRead is what one Git transfer brought in from its network remotes.
type networkRead struct {
	wroteStdout, wroteStderr bool
	// files and trees are the workspace paths the transfer filled.
	files, trees []string
}

// reportNetworkRead tells the program's provenance what a transfer from the
// admitted remotes delivered: ref names and remote messages in its output,
// and repository content in the files or tree it filled. Local and loopback
// repositories are not network sources.
func reportNetworkRead(ctx context.Context, admitted admittedRemotes, read networkRead) {
	var urls []string
	for _, raw := range admitted.urls {
		if u := provenanceURL(raw); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return
	}
	evidence.ReportExchangeProvenance(ctx, evidence.ExchangeProvenance{
		External: true, ObservedURLs: urls,
		WroteStdout: read.wroteStdout, WroteStderr: read.wroteStderr,
		WrittenFiles: read.files, WrittenTrees: read.trees,
	})
}

// provenanceURL names a network remote without credentials, query or fragment.
func provenanceURL(raw string) string {
	host := remoteNetworkHost(raw)
	if host == "" || egress.SyntacticLoopback(host) {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	}
	// scp-like syntax: [user@]host:path
	_, repoPath, _ := strings.Cut(raw, ":")
	return (&url.URL{Scheme: "ssh", Host: host, Path: "/" + strings.TrimPrefix(repoPath, "/")}).String()
}

// mergedPaths are the absolute paths a merge or rebase reviewed in the
// repository rooted at repoRoot.
func mergedPaths(repoRoot string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, filepath.Join(repoRoot, filepath.FromSlash(p)))
		}
	}
	return out
}
