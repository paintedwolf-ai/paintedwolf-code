package projectstack

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	StackDocPerFileCap = 8 * 1024
	StackDocTotalCap   = 24 * 1024
	StackMaxDocURLs    = 8
)

var docURLRE = regexp.MustCompile(`https://[^\s\])>'"]+`)

var stackDocRelPaths = []string{
	"README.md",
	"README",
	"AGENTS.md",
	"docs/README.md",
	"docs/index.md",
}

// docURLsFromRoot reads bounded doc files under root and returns unique https
// URLs suitable for crawl-only warming.
func docURLsFromRoot(root string) []string {
	var bodies []string
	total := 0
	for _, rel := range stackDocRelPaths {
		if total >= StackDocTotalCap {
			break
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G703 -- fixed relative paths under user root
		if err != nil {
			continue
		}
		if len(data) > StackDocPerFileCap {
			data = data[:StackDocPerFileCap]
		}
		remain := StackDocTotalCap - total
		if len(data) > remain {
			data = data[:remain]
		}
		total += len(data)
		bodies = append(bodies, string(data))
	}
	return extractHTTPSURLs(strings.Join(bodies, "\n"))
}

func extractHTTPSURLs(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, match := range docURLRE.FindAllString(text, -1) {
		u := strings.TrimSpace(strings.TrimRight(match, ".,;:)"))
		if u == "" || !docURLAllowed(u) {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
		if len(out) >= StackMaxDocURLs {
			break
		}
	}
	return out
}

func docURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			return false
		}
	}
	return true
}
