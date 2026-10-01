package detectionpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadBundledCatalog(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	want := []string{
		"ai-compute-platform", "analytics-warehouse", "app-backend-platform", "auth-platform",
		"aws-cli", "azure-cli", "azure-structured-actions", "backup-recovery", "bulk-data-export",
		"ci-cd-other", "cloud-hosting-other", "cms-platform", "command-destructive",
		"container-runtime", "credential-minting", "credential-stores", "database-cli", "directory-identity",
		"dns-cdn", "egress-providers", "email-platform", "exfil-sinks", "gcloud-cli", "gcp-structured-actions", "gitops",
		"host-system", "iac-other", "incident-observability", "key-material", "kubernetes-cli",
		"local-vm", "mdm", "media-platform", "message-queue", "mobile-release",
		"object-storage", "paas-hosting", "path-aliasing", "payment-platform", "publish-release",
		"salesforce-platform", "search-cluster", "secrets-tooling",
		"serverless-data", "serverless-edge", "source-control",
		"structured-action-outcomes",
		"storage-cluster", "supply-chain", "terraform-cli", "vector-database",
		"windows-admin",
	}
	if len(cat.Warnings) != 0 {
		t.Fatalf("warnings: %v", cat.Warnings)
	}
	got := map[string]Pack{}
	for _, p := range cat.Packs {
		got[p.ID] = p
	}
	for _, id := range want {
		p, ok := got[id]
		if !ok {
			t.Fatalf("missing pack %s", id)
		}
		wantSource := SourceBundled
		if strings.HasSuffix(id, "-structured-actions") {
			wantSource = SourceGenerated
		}
		if p.Source != wantSource {
			t.Fatalf("pack %s source=%s want %s", id, p.Source, wantSource)
		}
		if len(p.Rules) == 0 {
			t.Fatalf("pack %s has no rules", id)
		}
		for _, r := range p.Rules {
			if !r.Supported {
				t.Fatalf("pack %s rule %s unsupported: %s", id, r.Slug, r.UnsupportedReason)
			}
			if r.Slug == "" {
				t.Fatalf("pack %s rule missing slug", id)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("pack count = %d, want %d (%v)", len(got), len(want), keys(got))
	}
}

func keys(m map[string]Pack) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDevicePackCollisionRejected(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	dir := filepath.Join(DevicePacksDir(cfg), "aws-cli")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(`
id: aws-cli
label: Fake
description: Collision
`), 0o600))
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "x.yml"), []byte(`
title: X
id: 11111111-1111-4111-8111-111111111111
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: aws}
  condition: sel
`), 0o600))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	found := false
	for _, w := range cat.Warnings {
		// The refusal names both sides: which pack was dropped and who already
		// answers to that id, so the person can act on it.
		if strings.Contains(w, `detection pack "aws-cli" from the device detection folder was not loaded`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected collision warning, got %v", cat.Warnings)
	}
	p, ok := cat.PackByID("aws-cli")
	if !ok || p.Source != SourceBundled {
		t.Fatalf("bundled aws-cli missing: %+v", p)
	}
}

func TestDevicePackAdditive(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	dir := filepath.Join(DevicePacksDir(cfg), "my-custom")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(`
id: my-custom
label: Custom
description: A device pack
`), 0o600))
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "hit.yml"), []byte(`
title: Hit
id: 22222222-2222-4222-8222-222222222222
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: custombin}
  condition: sel
`), 0o600))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	p, ok := cat.PackByID("my-custom")
	if !ok {
		t.Fatal("missing device pack")
	}
	if p.Source != SourceDevice || !p.Enabled {
		t.Fatalf("pack = %+v", p)
	}
}

// Duplicate rule ids leave the first rule active.
func TestDuplicateRuleIDLeavesTheIncumbentRunning(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	dir := filepath.Join(DevicePacksDir(cfg), "dup-pack")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(`
id: dup-pack
label: Dup
description: d
`), 0o600))
	// Steal a UUID from command-destructive by reading one rule id.
	cat0, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog0", err)
	var stolen string
	for _, p := range cat0.Packs {
		if p.ID == "command-destructive" && len(p.Rules) > 0 {
			stolen = p.Rules[0].ID
			break
		}
	}
	if stolen == "" {
		t.Fatal("no command rule id")
	}
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "dup.yml"), []byte(`
title: Dup
id: `+stolen+`
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: zz}
  condition: sel
`), 0o600))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	var shippedKept, deviceInactive bool
	for _, p := range cat.Packs {
		for _, r := range p.Rules {
			if r.ID != stolen {
				continue
			}
			switch p.ID {
			case "command-destructive":
				shippedKept = r.Supported
			case "dup-pack":
				deviceInactive = !r.Supported &&
					strings.Contains(r.UnsupportedReason, "already used by") &&
					strings.Contains(r.UnsupportedReason, "command-destructive")
			}
		}
	}
	if !shippedKept || !deviceInactive {
		t.Fatalf("shippedKept=%v deviceInactive=%v", shippedKept, deviceInactive)
	}
}

// Precedence is merge order — who arrived first — not which pack id sorts
// lower, so the answer does not change when somebody names a pack `aaa-`.
func TestDuplicateRuleIDAcrossPacksKeepsTheFirstClaim(t *testing.T) {
	t.Parallel()
	const id = "22222222-2222-4222-8222-222222222222"
	rule := func(slug string) Rule {
		return Rule{ID: id, Slug: slug, Supported: true}
	}
	packs := []Pack{
		{ID: "zzz-shipped", Source: SourceBundled, Rules: []Rule{rule("winner")}},
		{ID: "aaa-installed", Source: SourceExtension, Rules: []Rule{rule("second")}},
		{ID: "bbb-device", Source: SourceDevice, Rules: []Rule{rule("third")}},
	}
	markDuplicateRuleIDs(packs)
	if !packs[0].Rules[0].Supported {
		t.Fatalf("the first claim must keep the id: %+v", packs[0].Rules[0])
	}
	for _, later := range packs[1:] {
		if later.Rules[0].Supported ||
			!strings.Contains(later.Rules[0].UnsupportedReason, "already used by winner in zzz-shipped") {
			t.Fatalf("later claim not held out: %+v", later.Rules[0])
		}
	}
}

// Duplicate rule IDs disable both rules within one pack.
func TestDuplicateRuleIDInsideOnePackDisablesBoth(t *testing.T) {
	t.Parallel()
	const id = "33333333-3333-4333-8333-333333333333"
	packs := []Pack{{ID: "one-pack", Source: SourceExtension, Rules: []Rule{
		{ID: id, Slug: "first", Supported: true},
		{ID: id, Slug: "second", Supported: true},
	}}}
	markDuplicateRuleIDs(packs)
	for i, r := range packs[0].Rules {
		if r.Supported || !strings.Contains(r.UnsupportedReason, "inside this pack") {
			t.Fatalf("definition %d = %+v", i, r)
		}
	}
}

func TestDisabledStateApplied(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	testutil.FailErr(t, "WriteDisabledIDs", WriteDisabledIDs(cfg, []string{"aws-cli"}))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	p, ok := cat.PackByID("aws-cli")
	if !ok || p.Enabled {
		t.Fatalf("aws-cli enabled=%v", p.Enabled)
	}
	m := NewMatcher(cat)
	hit, ok := m.Match(testEvent("command", "aws s3 rb s3://b", "/p", true, "proxy", "s"))
	if ok {
		t.Fatalf("matcher returned disabled pack hit: %+v", hit)
	}
}

func TestWriteDisabledIDsCanonicalizesAndRejectsInvalidIDs(t *testing.T) {
	t.Parallel()
	cfg := filepath.Join(t.TempDir(), "nested")
	if err := WriteDisabledIDs(cfg, []string{"gcloud-cli", "aws-cli", "gcloud-cli"}); err != nil {
		t.Fatalf("WriteDisabledIDs: %v", err)
	}
	data, err := os.ReadFile(DeviceStatePath(cfg))
	testutil.FailErr(t, "read disabled state", err)
	if got, want := string(data), "disabled:\n    - aws-cli\n    - gcloud-cli\n"; got != want {
		t.Fatalf("state=%q want %q", got, want)
	}
	if err := WriteDisabledIDs(cfg, []string{"not/a-pack"}); err == nil {
		t.Fatal("invalid pack id must be rejected")
	}
}

func TestMatcherSeverityOrder(t *testing.T) {
	t.Parallel()
	// Synthetic catalog: alphabetically-earlier pack has medium; later has critical for same command.
	cat := &Catalog{Packs: []Pack{
		{
			ID: "aaa-medium", Enabled: true, Source: SourceDevice,
			Rules: []Rule{mustRule(t, `title: Med
id: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa
description: d
logsource: {product: lycaon, service: tool_exec}
level: medium
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)},
		},
		{
			ID: "zzz-critical", Enabled: true, Source: SourceDevice,
			Rules: []Rule{mustRule(t, `title: Crit
id: bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb
description: d
logsource: {product: lycaon, service: tool_exec}
level: critical
detection:
  sel: {CommandLine|contains: ' boom '}
  condition: sel`)},
		},
	}}
	m := NewMatcher(cat)
	hit, ok := m.Match(testEvent("command", "do boom now", "/p", true, "proxy", "s"))
	if !ok || hit.Level != LevelCritical || hit.PackID != "zzz-critical" {
		t.Fatalf("hit=%+v ok=%v", hit, ok)
	}
}

func TestMatcherPanicRecovered(t *testing.T) {
	t.Parallel()
	r := mustRule(t, `title: Panic
id: cccccccc-cccc-4ccc-8ccc-cccccccccccc
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: x}
  condition: sel`)
	r.condition = panicNode{}
	cat := &Catalog{Packs: []Pack{{
		ID: "panic-pack", Enabled: true, Source: SourceDevice, Rules: []Rule{r},
	}}}
	m := NewMatcher(cat)
	_, ok := m.Match(testEvent("command", "x", "/p", true, "proxy", "s"))
	if ok {
		t.Fatal("expected no match after panic")
	}
	// Second call still ok (rule skipped).
	_, ok = m.Match(testEvent("command", "x", "/p", true, "proxy", "s"))
	if ok {
		t.Fatal("expected continued skip")
	}
}

type panicNode struct{}

func (panicNode) eval(map[string]selection, func(string) (any, bool)) bool {
	panic("injected")
}

func TestMatcherEmptyCatalog(t *testing.T) {
	t.Parallel()
	var m *Matcher
	if _, ok := m.Match(Event{}); ok {
		t.Fatal("nil matcher")
	}
	m = NewMatcher(nil)
	if _, ok := m.Match(Event{}); ok {
		t.Fatal("nil catalog")
	}
	m = NewMatcher(&Catalog{})
	if _, ok := m.Match(Event{}); ok {
		t.Fatal("empty catalog")
	}
}

func mustRule(t *testing.T, yaml string) Rule {
	t.Helper()
	r, err := ParseRule([]byte(yaml))
	testutil.FailErr(t, "ParseRule", err)
	if !r.Supported {
		t.Fatalf("unsupported: %s", r.UnsupportedReason)
	}
	return r
}
