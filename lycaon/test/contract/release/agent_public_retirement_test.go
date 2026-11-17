package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/retiredids"
	"gopkg.in/yaml.v3"
)

// TestDisappearedAgentPublicIDsAreRecorded compares the ids that shipped in the
// last release with HEAD; one that disappeared needs a retired ledger entry.
func TestDisappearedAgentPublicIDsAreRecorded(t *testing.T) {
	t.Parallel()
	tag := lastReleasedTagRef(t)
	led := retiredids.Load(t)
	// Both sides use the same parsers: the live registries count ids differently.
	atRef := func(rev string) map[string]map[string]struct{} {
		return map[string]map[string]struct{}{
			"hint_code":    hintCodesAtRef(t, rev),
			"tool_name":    toolNamesAtRef(t, rev),
			"rule_effect":  ruleEffectsAtRef(t, rev),
			"theme_token":  themeTokensAtRef(t, rev),
			"virtual_root": virtualRootsAtRef(t, rev),
		}
	}
	atTag, atHead := atRef(tag), atRef("HEAD")
	for _, kind := range retiredids.Kinds {
		if len(atTag[kind]) == 0 {
			t.Errorf("no %s ids parsed at %s; the comparison would pass on nothing", kind, tag)
		}
		for _, id := range retiredids.UnrecordedDisappearances(led, kind, atTag[kind], atHead[kind]) {
			t.Errorf("%s %q was present at %s and is gone at HEAD but not in %s; append a retired entry with kind: %s",
				kind, id, tag, retiredids.LedgerRel, kind)
		}
	}
}

// lastReleasedTagRef returns the most recent v* tag reachable from HEAD. On
// trunk the merge base is HEAD, so that tag is the last installed build.
func lastReleasedTagRef(t *testing.T) string {
	t.Helper()
	out := gitOutput(t, "describe", "--tags", "--abbrev=0", "--match", "v*")
	tag := strings.TrimSpace(string(out))
	if tag == "" {
		t.Fatal("no release tag reachable from HEAD; fetch tags before running release contracts")
	}
	return tag
}

func gitOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = contractcheck.RepoRoot(t)
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	contractcheck.FailErr(t, "git "+strings.Join(args, " "), err)
	return out
}

func gitShowFile(t *testing.T, rev, relPath string) []byte {
	t.Helper()
	return gitOutput(t, "show", rev+":"+relPath)
}

func hintCodesAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	dir := materializePolicyAtRef(t, rev)
	cfg, err := guidance.LoadHintConfig(extpacks.OnDisk(dir))
	contractcheck.FailErr(t, "LoadHintConfig at "+rev, err)
	out := make(map[string]struct{}, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		out[code] = struct{}{}
	}
	return out
}

func toolNamesAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	// nativemanifest.Load reads the embedded HEAD manifest, so parse the bytes at rev.
	raw := gitShowFile(t, rev, "lycaon/config/"+config.NativeTools.String())
	var cfg nativemanifest.Config
	contractcheck.FailErr(t, "parse native-tools at "+rev, yaml.Unmarshal(raw, &cfg))
	out := map[string]struct{}{}
	for _, name := range cfg.AllTools() {
		out[name] = struct{}{}
	}
	for _, name := range cfg.HostProducedTools {
		out[name] = struct{}{}
	}
	for _, list := range cfg.Families {
		for _, name := range list {
			out[name] = struct{}{}
		}
	}
	for _, list := range cfg.ResourceImplied {
		for _, name := range list {
			out[name] = struct{}{}
		}
	}
	for _, name := range tools.BootToolClaimNames() {
		out[name] = struct{}{}
	}
	return out
}

func ruleEffectsAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	dir := materializePolicyAtRef(t, rev)
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read dir policy at "+rev, err)
	out := map[string]struct{}{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		contractcheck.FailErr(t, "read policy "+entry.Name()+" at "+rev, err)
		var doc struct {
			Effect string `yaml:"effect"`
		}
		contractcheck.FailErr(t, "parse policy "+entry.Name()+" at "+rev, yaml.Unmarshal(raw, &doc))
		if eff := strings.TrimSpace(doc.Effect); eff != "" {
			out[eff] = struct{}{}
		}
	}
	return out
}

func themeTokensAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	raw := gitShowFile(t, rev, "lycaon-den/src/contributions/theme-vocabulary.generated.ts")
	out := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"") {
			if idx := strings.Index(line[1:], "\""); idx > 0 {
				out[line[1:idx+1]] = struct{}{}
			}
		}
	}
	return out
}

func virtualRootsAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	raw := gitShowFile(t, rev, "lycaon/internal/projectroot/roots.go")
	out := map[string]struct{}{}
	if strings.Contains(string(raw), "VirtualScratchLabel") {
		out["scratch"] = struct{}{}
	}
	return out
}

// materializePolicyAtRef flattens every bundled pack's policy YAML at rev into
// one directory LoadHintConfig can read.
func materializePolicyAtRef(t *testing.T, rev string) string {
	t.Helper()
	out := gitOutput(t, "ls-tree", "-r", "--name-only", rev, "--", "lycaon/config/packs/painted-wolf")
	dir := t.TempDir()
	for _, rel := range strings.Split(string(out), "\n") {
		rel = strings.TrimSpace(rel)
		if !strings.HasSuffix(rel, ".yaml") || !strings.Contains(rel, "/policy/") {
			continue
		}
		dest := filepath.Join(dir, filepath.Base(rel))
		contractcheck.FailErr(t, "write "+dest, os.WriteFile(dest, gitShowFile(t, rev, rel), 0o600))
	}
	return dir
}
