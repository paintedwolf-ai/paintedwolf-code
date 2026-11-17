package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/theme"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/retiredids"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

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
	for _, eff := range []oar.Effect{oar.EffectBlock, oar.EffectWarn, oar.EffectNudge, oar.EffectAllow} {
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
	for _, e := range retiredids.Collisions(retiredids.Load(t), liveIDsByKind(t)) {
		t.Errorf("retired %s %q is live again; a retired id cannot be reused", e.Kind, e.ID)
	}
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
	led := retiredids.Ledger{Retired: []retiredids.Entry{{ID: sample, Kind: "hint_code"}, {ID: sample, Kind: "tool_name"}}}
	got := retiredids.Collisions(led, live)
	if len(got) != 1 || got[0].Kind != "hint_code" {
		t.Fatalf("collisions = %+v, want only the hint_code entry for %q", got, sample)
	}
}

func TestDisappearanceRequiresMatchingKind(t *testing.T) {
	t.Parallel()
	led := retiredids.Ledger{Retired: []retiredids.Entry{{ID: "GONE", Kind: "hint_code"}}}
	base := map[string]struct{}{"GONE": {}, "KEPT": {}}
	head := map[string]struct{}{"KEPT": {}}
	if got := retiredids.UnrecordedDisappearances(led, "hint_code", base, head); len(got) != 0 {
		t.Fatalf("hint_code disappearances = %q, want none", got)
	}
	if got := retiredids.UnrecordedDisappearances(led, "tool_name", base, head); !slices.Equal(got, []string{"GONE"}) {
		t.Fatalf("tool_name disappearances = %q, want [GONE]", got)
	}
}
