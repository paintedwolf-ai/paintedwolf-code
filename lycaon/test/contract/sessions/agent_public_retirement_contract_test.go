package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/theme"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
	"gopkg.in/yaml.v3"
)

type retiredLedger struct {
	Retired []retiredEntry `yaml:"retired"`
}

type retiredEntry struct {
	ID      string `yaml:"id"`
	Kind    string `yaml:"kind"`
	Summary string `yaml:"summary"`
	Date    string `yaml:"date"`
}

const agentPublicRetiredRel = "docs/agent-public-retired.yaml"

var retiredKinds = []string{"hint_code", "tool_name", "rule_effect", "theme_token", "virtual_root"}

func loadRetiredLedger(t *testing.T) retiredLedger {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), agentPublicRetiredRel))
	contractcheck.FailErr(t, "read "+agentPublicRetiredRel, err)
	var led retiredLedger
	contractcheck.FailErr(t, "parse "+agentPublicRetiredRel, yaml.Unmarshal(raw, &led))
	for i, e := range led.Retired {
		if strings.TrimSpace(e.ID) == "" {
			t.Fatalf("retired[%d]: id required", i)
		}
		if !slices.Contains(retiredKinds, e.Kind) {
			t.Fatalf("retired[%d] id=%s: kind %q want one of %v", i, e.ID, e.Kind, retiredKinds)
		}
	}
	return led
}

// retiredCollisions lists ledger entries whose id is live under the same kind.
func retiredCollisions(led retiredLedger, live map[string]map[string]struct{}) []retiredEntry {
	var out []retiredEntry
	for _, e := range led.Retired {
		if _, ok := live[e.Kind][e.ID]; ok {
			out = append(out, e)
		}
	}
	return out
}

// unrecordedDisappearances lists ids in base that head lacks and the ledger
// does not record under kind.
func unrecordedDisappearances(led retiredLedger, kind string, base, head map[string]struct{}) []string {
	recorded := map[string]bool{}
	for _, e := range led.Retired {
		if e.Kind == kind {
			recorded[e.ID] = true
		}
	}
	var out []string
	for id := range base {
		if _, ok := head[id]; ok || recorded[id] {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func liveIDsByKind(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	return map[string]map[string]struct{}{
		"hint_code":    liveHintCodes(t),
		"tool_name":    liveToolNames(t),
		"rule_effect":  liveRuleEffects(t),
		"theme_token":  liveThemeTokens(t),
		"virtual_root": liveVirtualRoots(t),
	}
}

func liveHintCodes(t *testing.T) map[string]struct{} {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	out := make(map[string]struct{}, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		out[code] = struct{}{}
	}
	return out
}

func liveToolNames(t *testing.T) map[string]struct{} {
	t.Helper()
	reg := toolfixture.ContractServeBootRegistry(t)
	toolSet := toolfixture.BootRegisteredToolSet(t, reg)
	out := make(map[string]struct{}, len(toolSet))
	for name := range toolSet {
		out[name] = struct{}{}
	}
	return out
}

func liveRuleEffects(t *testing.T) map[string]struct{} {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	out := map[string]struct{}{
		"inform": {}, // anchor binding effect; it has no typed constant
	}
	for _, eff := range []oar.Effect{oar.EffectBlock, oar.EffectWarn, oar.EffectNudge, oar.EffectAllow, oar.EffectTransform} {
		out[string(eff)] = struct{}{}
	}
	for _, eff := range []settings.ApprovalEffect{settings.ApprovalEffectAsk, settings.ApprovalEffectDeny} {
		out[string(eff)] = struct{}{}
	}
	for _, entry := range cfg.HintCodes {
		if eff := strings.TrimSpace(entry.Effect); eff != "" {
			out[eff] = struct{}{}
		}
	}
	return out
}

func liveThemeTokens(t *testing.T) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, tok := range theme.BaseTokens() {
		out[tok.ID] = struct{}{}
	}
	for _, scope := range theme.SyntaxScopes() {
		out[scope.ID] = struct{}{}
	}
	return out
}

func liveVirtualRoots(t *testing.T) map[string]struct{} {
	t.Helper()
	return map[string]struct{}{
		projectroot.VirtualScratchLabel: {},
	}
}

func TestRetiredIDsAreNotLive(t *testing.T) {
	t.Parallel()
	for _, e := range retiredCollisions(loadRetiredLedger(t), liveIDsByKind(t)) {
		t.Errorf("retired %s %q is live again; a retired id cannot be reused", e.Kind, e.ID)
	}
}

func TestDisappearedAgentPublicIDsAreRecorded(t *testing.T) {
	tag := lastReleasedTagRef(t)
	if tag == "" {
		// Ids that never shipped need no ledger entry.
		return
	}
	led := loadRetiredLedger(t)
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
	for _, kind := range retiredKinds {
		for _, id := range unrecordedDisappearances(led, kind, atTag[kind], atHead[kind]) {
			t.Errorf("%s %q was present at %s and is gone at HEAD but not in %s; append a retired entry with kind: %s",
				kind, id, tag, agentPublicRetiredRel, kind)
		}
	}
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
	rel := "lycaon/config/" + config.NativeTools.String()
	raw, ok := gitShowFile(t, rev, rel)
	if !ok {
		t.Fatalf("native-tools.yaml missing at %s (%s)", rev, rel)
	}
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
		if err != nil {
			continue
		}
		var doc struct {
			Effect string `yaml:"effect"`
		}
		if err := yaml.Unmarshal(raw, &doc); err == nil && strings.TrimSpace(doc.Effect) != "" {
			out[strings.TrimSpace(doc.Effect)] = struct{}{}
		}
	}
	return out
}

func themeTokensAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	rel := "lycaon-den/src/contributions/theme-vocabulary.generated.ts"
	raw, ok := gitShowFile(t, rev, rel)
	if !ok {
		t.Fatalf("theme vocabulary missing at %s (%s)", rev, rel)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"") {
			if idx := strings.Index(line[1:], "\""); idx > 0 {
				tokenID := line[1 : idx+1]
				out[tokenID] = struct{}{}
			}
		}
	}
	return out
}

func virtualRootsAtRef(t *testing.T, rev string) map[string]struct{} {
	t.Helper()
	rel := "lycaon/internal/projectroot/roots.go"
	raw, ok := gitShowFile(t, rev, rel)
	if !ok {
		t.Fatalf("project roots missing at %s (%s)", rev, rel)
	}
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
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", rev, "--",
		"lycaon/config/packs/painted-wolf")
	cmd.Dir = contractcheck.RepoRoot(t)
	cmd.Env = lyexec.LocalGitEnv()
	out, err := cmd.Output()
	contractcheck.FailErr(t, "ls-tree policy at "+rev, err)
	dir := t.TempDir()
	for _, rel := range strings.Split(string(out), "\n") {
		rel = strings.TrimSpace(rel)
		if !strings.HasSuffix(rel, ".yaml") || !strings.Contains(rel, "/policy/") {
			continue
		}
		body, ok := gitShowFile(t, rev, rel)
		if !ok {
			continue
		}
		dest := filepath.Join(dir, filepath.Base(rel))
		contractcheck.FailErr(t, "write "+dest, os.WriteFile(dest, body, 0o600))
	}
	return dir
}

func TestRetiredCollisionMatchesKind(t *testing.T) {
	t.Parallel()
	live := map[string]map[string]struct{}{"hint_code": liveHintCodes(t)}
	var sample string
	for code := range live["hint_code"] {
		sample = code
		break
	}
	if sample == "" {
		t.Fatal("no live hint codes to probe")
	}
	led := retiredLedger{Retired: []retiredEntry{{ID: sample, Kind: "hint_code"}, {ID: sample, Kind: "tool_name"}}}
	got := retiredCollisions(led, live)
	if len(got) != 1 || got[0].Kind != "hint_code" {
		t.Fatalf("collisions = %+v, want only the hint_code entry for %q", got, sample)
	}
}

func TestDisappearanceRequiresMatchingKind(t *testing.T) {
	t.Parallel()
	led := retiredLedger{Retired: []retiredEntry{{ID: "GONE", Kind: "hint_code"}}}
	base := map[string]struct{}{"GONE": {}, "KEPT": {}}
	head := map[string]struct{}{"KEPT": {}}
	if got := unrecordedDisappearances(led, "hint_code", base, head); len(got) != 0 {
		t.Fatalf("hint_code disappearances = %q, want none", got)
	}
	if got := unrecordedDisappearances(led, "tool_name", base, head); !slices.Equal(got, []string{"GONE"}) {
		t.Fatalf("tool_name disappearances = %q, want [GONE]", got)
	}
}
