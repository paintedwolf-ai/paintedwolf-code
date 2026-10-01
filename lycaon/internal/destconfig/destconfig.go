// Package destconfig attests outbound hosts from user-controlled device settings.
// Repository content and bundled catalogs cannot supply this approval fact.
package destconfig

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// Source resolves user-configured hosts for a project or the whole device.
type Source struct {
	// Name identifies the configuration source on the approval card.
	Name        string
	Hosts       func(projectDir string) []string
	HostSources func(projectDir string) map[string]string
}

// Registry resolves a host against every wired source.
type Registry struct {
	mu      sync.RWMutex
	sources []Source
	cache   map[string]cacheEntry
	ttl     time.Duration
	now     func() time.Time
}

type cacheEntry struct {
	hosts map[string]string // host → source name
	at    time.Time
}

// NewRegistry caches per-project lookups briefly so settings changes become visible.
func NewRegistry() *Registry {
	return &Registry{cache: map[string]cacheEntry{}, ttl: 30 * time.Second, now: time.Now}
}

// Add wires a source. Sources with no lookup are ignored.
func (r *Registry) Add(s Source) {
	if r == nil || (s.Hosts == nil && s.HostSources == nil) {
		return
	}
	if s.Hosts != nil && strings.TrimSpace(s.Name) == "" && s.HostSources == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, s)
	r.cache = map[string]cacheEntry{}
}

// Configured reports whether the user's configuration names host for this
// project, and which source said so.
func (r *Registry) Configured(projectDir, host string) (string, bool) {
	if r == nil {
		return "", false
	}
	host = Normalize(host)
	if host == "" {
		return "", false
	}
	name, ok := r.hostsFor(projectDir)[host]
	return name, ok
}

func (r *Registry) hostsFor(projectDir string) map[string]string {
	r.mu.RLock()
	entry, ok := r.cache[projectDir]
	fresh := ok && r.now().Sub(entry.at) < r.ttl
	sources := r.sources
	r.mu.RUnlock()
	if fresh {
		return entry.hosts
	}

	hosts := map[string]string{}
	for _, s := range sources {
		if s.HostSources != nil {
			for raw, srcName := range s.HostSources(projectDir) {
				if h := Normalize(raw); h != "" {
					if _, taken := hosts[h]; !taken {
						hosts[h] = srcName
					}
				}
			}
		}
		if s.Hosts != nil {
			for _, raw := range s.Hosts(projectDir) {
				if h := Normalize(raw); h != "" {
					if _, taken := hosts[h]; !taken {
						hosts[h] = s.Name
					}
				}
			}
		}
	}
	r.mu.Lock()
	r.cache[projectDir] = cacheEntry{hosts: hosts, at: r.now()}
	r.mu.Unlock()
	return hosts
}

// GitRemoteHostsForRoots reads git remotes from each repository root and returns host → "git:<remote>".
func GitRemoteHostsForRoots(roots ...string) map[string]string {
	remotes := make(map[string]string)
	seenRoots := make(map[string]bool)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		list, ok := gitexec.ListRemotes(context.Background(), root)
		if ok {
			for _, r := range list {
				host := parseGitRemoteHost(r.URL)
				if host != "" {
					if _, taken := remotes[host]; !taken {
						remotes[host] = "git:" + r.Name
					}
				}
			}
			continue
		}
		for h, src := range fallbackGitConfigRemotes(root) {
			if _, taken := remotes[h]; !taken {
				remotes[h] = src
			}
		}
	}
	return remotes
}

func fallbackGitConfigRemotes(projectDir string) map[string]string {
	if projectDir == "" {
		return nil
	}
	gitDir := filepath.Join(projectDir, ".git")
	fi, err := os.Stat(gitDir)
	if err != nil {
		return nil
	}
	configPath := filepath.Join(gitDir, "config")
	if !fi.IsDir() {
		data, err := os.ReadFile(gitDir) // #nosec G304
		if err != nil {
			return nil
		}
		line := strings.TrimSpace(string(data))
		if strings.HasPrefix(line, "gitdir:") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
			if !filepath.IsAbs(target) {
				target = filepath.Join(projectDir, target)
			}
			configPath = filepath.Join(target, "config")
		}
	}
	data, err := os.ReadFile(configPath) // #nosec G304 G703 -- the config the repository's own .git entry names, resolved as git resolves it
	if err != nil {
		return nil
	}
	return parseGitConfigRemotes(string(data))
}

func parseGitConfigRemotes(configText string) map[string]string {
	remotes := make(map[string]string)
	currentRemote := ""
	scanner := bufio.NewScanner(strings.NewReader(configText))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[remote \"") && strings.HasSuffix(line, "\"]") {
			currentRemote = strings.TrimSuffix(strings.TrimPrefix(line, "[remote \""), "\"]")
			continue
		}
		if strings.HasPrefix(line, "[") {
			currentRemote = ""
			continue
		}
		if currentRemote != "" && strings.HasPrefix(line, "url") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == "url" {
				rawURL := strings.TrimSpace(parts[1])
				host := parseGitRemoteHost(rawURL)
				if host != "" {
					remotes[host] = "git:" + currentRemote
				}
			}
		}
	}
	return remotes
}

func parseGitRemoteHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// SCP-like syntax: [user@]host:path
	if strings.Contains(raw, "@") && strings.Contains(raw, ":") && !strings.Contains(raw, "://") {
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if colon > at {
			host := strings.TrimSpace(raw[at+1 : colon])
			return strings.ToLower(host)
		}
	}
	return Normalize(raw)
}

// Normalize reduces an endpoint to the hostname the egress broker observes. It
// accepts a bare host or a URL, the two shapes the user's configuration holds.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(u.Hostname()))
	}
	raw = strings.TrimSuffix(raw, ".")
	if raw == "" || strings.ContainsAny(raw, "/ \t") {
		return ""
	}
	return strings.ToLower(raw)
}
