package packboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

type toolchainDef struct {
	binary   string
	args     []string
	language string
	// label names the tool in the reported version, e.g. "python" for python3.
	label string
}

var semverRegexp = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.-]+)?)`)

// knownToolchains is ordered by report priority.
var knownToolchains = []toolchainDef{
	{binary: "bun", args: []string{"--version"}, language: "JavaScript", label: "bun"},
	{binary: "node", args: []string{"--version"}, language: "JavaScript", label: "node"},
	{binary: "pnpm", args: []string{"--version"}, language: "JavaScript", label: "pnpm"},
	{binary: "yarn", args: []string{"--version"}, language: "JavaScript", label: "yarn"},
	{binary: "deno", args: []string{"--version"}, language: "JavaScript", label: "deno"},
	{binary: "python3", args: []string{"--version"}, language: "Python", label: "python"},
	{binary: "uv", args: []string{"--version"}, language: "Python", label: "uv"},
	{binary: "poetry", args: []string{"--version"}, language: "Python", label: "poetry"},
	{binary: "go", args: []string{"version"}, language: "Go", label: "go"},
	{binary: "rustc", args: []string{"--version"}, language: "Rust", label: "rustc"},
	{binary: "cargo", args: []string{"--version"}, language: "Rust", label: "cargo"},
	{binary: "ruby", args: []string{"--version"}, language: "Ruby", label: "ruby"},
	{binary: "bundle", args: []string{"--version"}, language: "Ruby", label: "bundle"},
	{binary: "java", args: []string{"-version"}, language: "Java", label: "java"},
	{binary: "mvn", args: []string{"--version"}, language: "Java", label: "mvn"},
	{binary: "gradle", args: []string{"--version"}, language: "Java", label: "gradle"},
}

// parse extracts the reported version from a tool's version output.
func (tc toolchainDef) parse(out string) string {
	if tc.binary == "go" {
		// `go version` prints "go version go1.26.6 os/arch"; the toolchain token carries the version.
		for _, field := range strings.Fields(out) {
			if strings.HasPrefix(field, "go") && len(field) > 2 && field[2] >= '0' && field[2] <= '9' {
				return "go " + strings.TrimPrefix(field, "go")
			}
		}
	}
	if m := semverRegexp.FindString(out); m != "" {
		return tc.label + " " + strings.TrimPrefix(m, "v")
	}
	return ""
}

const (
	toolchainProbeTimeout  = 500 * time.Millisecond
	toolchainProbeBudget   = 2 * time.Second
	toolchainResultTTL     = time.Hour
	toolchainRetryInterval = time.Minute
	// Without a language focus, the report names the first few toolchains by priority.
	unfocusedToolchainLimit = 4
)

// toolchainHost is how a probe finds and runs binaries; tests replace it.
type toolchainHost struct {
	pathValue func() string
	lookPath  func(name, pathValue string) (string, error)
	version   func(ctx context.Context, binary string, args []string) string
}

var defaultToolchainHost = toolchainHost{
	pathValue: exec.EffectivePathValue,
	lookPath:  exec.LookPathIn,
	version: func(ctx context.Context, binary string, args []string) string {
		out, _, _ := exec.Run(ctx, binary, args, exec.ExecOpts{
			Launch:         exec.HostLaunch("toolchain_version_probe"),
			Timeout:        toolchainProbeTimeout,
			MaxOutputBytes: 4096,
		})
		return string(out)
	},
}

type toolchainResult struct {
	toolchains []string
	completed  time.Time
}

// toolchainProbes caches completed probes per resolved PATH and language focus.
type toolchainProbes struct {
	host toolchainHost
	mu   sync.Mutex
	// results holds only probes that ran to completion.
	results  map[string]toolchainResult
	inflight map[string]bool
	// failed records when an interrupted probe may be retried.
	failed map[string]time.Time
	// wg tracks background refreshes; tests wait on it.
	wg sync.WaitGroup
}

func newToolchainProbes(host toolchainHost) *toolchainProbes {
	return &toolchainProbes{host: host, results: map[string]toolchainResult{}, inflight: map[string]bool{}, failed: map[string]time.Time{}}
}

var sharedToolchainProbes = newToolchainProbes(defaultToolchainHost)

// Toolchains returns the last completed probe of local toolchains for the
// languages under the engine's resolved PATH. It never blocks: a missing or
// expired result schedules a background refresh and the caller keeps the last
// completed value, or none.
func Toolchains(ctx context.Context, languages []string) []string {
	return sharedToolchainProbes.toolchains(ctx, languages)
}

func (p *toolchainProbes) toolchains(ctx context.Context, languages []string) []string {
	focus := normalizeLanguages(languages)
	pathValue := p.host.pathValue()
	key := pathHash(pathValue) + ":" + strings.Join(focus, ",")
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	result, known := p.results[key]
	retryAt, failed := p.failed[key]
	stale := !known || now.Sub(result.completed) >= toolchainResultTTL
	if stale && !p.inflight[key] && (!failed || !now.Before(retryAt)) {
		p.inflight[key] = true
		p.wg.Add(1)
		// Detached so a finished prompt does not cancel the refresh; the budget bounds it.
		go p.refresh(context.WithoutCancel(ctx), key, pathValue, focus)
	}
	return append([]string(nil), result.toolchains...)
}

func (p *toolchainProbes) refresh(parent context.Context, key, pathValue string, focus []string) {
	defer p.wg.Done()
	ctx, cancel := context.WithTimeout(parent, toolchainProbeBudget)
	defer cancel()
	found, complete := p.probe(ctx, pathValue, focus)

	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inflight, key)
	if !complete {
		// A version query cut short says nothing about the toolchain; keep the last result.
		p.failed[key] = time.Now().Add(toolchainRetryInterval)
		slog.DebugContext(parent, "toolchain probe interrupted", "path_hash", pathHash(pathValue))
		return
	}
	delete(p.failed, key)
	p.results[key] = toolchainResult{toolchains: found, completed: time.Now()}
}

// probe runs every candidate in parallel and reports in priority order. It is
// complete only when no version query was cut short by its timeout or the budget.
func (p *toolchainProbes) probe(ctx context.Context, pathValue string, focus []string) ([]string, bool) {
	languages := make(map[string]bool, len(focus))
	for _, language := range focus {
		languages[language] = true
	}
	versions := make([]string, len(knownToolchains))
	var interrupted atomic.Bool
	var wg sync.WaitGroup
	for i, tc := range knownToolchains {
		if len(languages) > 0 && !languages[strings.ToLower(tc.language)] {
			continue
		}
		binary, err := p.host.lookPath(tc.binary, pathValue)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, toolchainProbeTimeout)
			defer cancel()
			versions[i] = tc.parse(p.host.version(probeCtx, binary, tc.args))
			if probeCtx.Err() != nil {
				interrupted.Store(true)
			}
		}()
	}
	wg.Wait()
	if interrupted.Load() || ctx.Err() != nil {
		return nil, false
	}

	var out []string
	seen := make(map[string]bool)
	for _, version := range versions {
		if version == "" || seen[version] {
			continue
		}
		seen[version] = true
		out = append(out, version)
		if len(languages) == 0 && len(out) >= unfocusedToolchainLimit {
			break
		}
	}
	return out, true
}

// normalizeLanguages maps repository languages onto the toolchain families that serve them.
func normalizeLanguages(languages []string) []string {
	set := make(map[string]bool, len(languages))
	for _, language := range languages {
		normalized := strings.ToLower(strings.TrimSpace(language))
		if normalized == "" {
			continue
		}
		set[normalized] = true
		switch normalized {
		case "typescript":
			set["javascript"] = true
		case "kotlin", "scala", "groovy":
			set["java"] = true
		}
	}
	out := make([]string, 0, len(set))
	for language := range set {
		out = append(out, language)
	}
	sort.Strings(out)
	return out
}

func pathHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:8])
}
