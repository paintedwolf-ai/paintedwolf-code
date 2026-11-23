package packboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
)

type toolchainDef struct {
	binary   string
	args     []string
	language string
}

var semverRegexp = regexp.MustCompile(`v?(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.-]+)?)`)

// knownToolchains is ordered by report priority.
var knownToolchains = []toolchainDef{
	{binary: "bun", args: []string{"--version"}, language: "JavaScript"},
	{binary: "node", args: []string{"--version"}, language: "JavaScript"},
	{binary: "pnpm", args: []string{"--version"}, language: "JavaScript"},
	{binary: "yarn", args: []string{"--version"}, language: "JavaScript"},
	{binary: "deno", args: []string{"--version"}, language: "JavaScript"},
	{binary: "python3", args: []string{"--version"}, language: "Python"},
	{binary: "uv", args: []string{"--version"}, language: "Python"},
	{binary: "poetry", args: []string{"--version"}, language: "Python"},
	{binary: "go", args: []string{"version"}, language: "Go"},
	{binary: "rustc", args: []string{"--version"}, language: "Rust"},
	{binary: "cargo", args: []string{"--version"}, language: "Rust"},
	{binary: "ruby", args: []string{"--version"}, language: "Ruby"},
	{binary: "bundle", args: []string{"--version"}, language: "Ruby"},
	{binary: "java", args: []string{"-version"}, language: "Java"},
	{binary: "mvn", args: []string{"--version"}, language: "Java"},
	{binary: "gradle", args: []string{"--version"}, language: "Java"},
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
		return tc.binary + " " + strings.TrimPrefix(m, "v")
	}
	return ""
}

const (
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
	version   func(ctx context.Context, binary string, args []string) (string, error)
}

var defaultToolchainHost = toolchainHost{
	pathValue: exec.EffectivePathValue,
	lookPath:  exec.LookPathIn,
	version: func(ctx context.Context, binary string, args []string) (string, error) {
		out, _, err := exec.Run(ctx, binary, args, exec.ExecOpts{
			Launch:         exec.HostLaunch("toolchain_version_probe"),
			MaxOutputBytes: 4096,
		})
		return string(out), err
	},
}

type toolchainAnswer struct {
	version string
	due     time.Time
	backoff time.Duration
}

type toolchainResult struct {
	answers  []toolchainAnswer
	inflight bool
}

// toolchainProbes caches answers per resolved PATH and language focus.
type toolchainProbes struct {
	host    toolchainHost
	mu      sync.Mutex
	results map[string]toolchainResult
	wg      sync.WaitGroup
}

func newToolchainProbes(host toolchainHost) *toolchainProbes {
	return &toolchainProbes{host: host, results: map[string]toolchainResult{}}
}

var sharedToolchainProbes = newToolchainProbes(defaultToolchainHost)

// Toolchains returns cached toolchain versions and refreshes due candidates asynchronously.
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
	result := p.results[key]
	if result.answers == nil {
		result.answers = make([]toolchainAnswer, len(knownToolchains))
	}
	var due []int
	var versions []string
	for i, answer := range result.answers {
		if len(focus) != 0 && !slices.Contains(focus, strings.ToLower(knownToolchains[i].language)) {
			continue
		}
		if !result.inflight && !now.Before(answer.due) {
			due = append(due, i)
		}
		if answer.version != "" && (len(focus) != 0 || len(versions) < unfocusedToolchainLimit) {
			versions = append(versions, answer.version)
		}
	}
	if len(due) != 0 {
		result.inflight = true
		p.results[key] = result
		p.wg.Go(func() {
			p.refresh(context.WithoutCancel(ctx), key, pathValue, slices.Clone(result.answers), due)
		})
	}
	return versions
}

func (p *toolchainProbes) refresh(parent context.Context, key, pathValue string, answers []toolchainAnswer, due []int) {
	ctx, cancel := context.WithTimeout(parent, toolchainProbeBudget)
	defer cancel()
	var wg sync.WaitGroup
	for _, i := range due {
		wg.Go(func() {
			answers[i] = p.probe(ctx, pathValue, knownToolchains[i], answers[i].backoff)
		})
	}
	wg.Wait()

	p.mu.Lock()
	p.results[key] = toolchainResult{answers: answers}
	p.mu.Unlock()
}

func (p *toolchainProbes) probe(ctx context.Context, pathValue string, tc toolchainDef, previousBackoff time.Duration) toolchainAnswer {
	binary, err := p.host.lookPath(tc.binary, pathValue)
	if err != nil {
		return toolchainAnswer{due: time.Now().Add(toolchainResultTTL)}
	}
	output, err := p.host.version(ctx, binary, tc.args)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, exec.ErrTimeout) {
		backoff := min(toolchainResultTTL, max(toolchainRetryInterval, previousBackoff*2))
		return toolchainAnswer{due: time.Now().Add(backoff), backoff: backoff}
	}
	return toolchainAnswer{version: tc.parse(output), due: time.Now().Add(toolchainResultTTL)}
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
