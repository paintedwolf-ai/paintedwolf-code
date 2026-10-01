package detectionpack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeMinimalPack(t *testing.T, dir, id string, rules map[string]string) {
	t.Helper()
	_ = os.RemoveAll(dir)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(
		"id: "+id+"\nlabel: L\ndescription: D\n",
	), 0o600))
	for slug, body := range rules {
		testutil.FailErr(t, "rule "+slug, os.WriteFile(filepath.Join(dir, "rules", slug+".yml"), []byte(body), 0o600))
	}
}

func goodRule(slugTitle string) string {
	return `title: ` + slugTitle + `
id: 33333333-3333-4333-8333-333333333331
description: consequence
logsource:
  product: lycaon
  service: tool_exec
level: high
detection:
  sel:
    Image: demobin
  condition: sel
`
}

func TestImportDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "demo-pack", map[string]string{"hit": goodRule("Hit")})
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
	testutil.FailErr(t, "ImportPack", err)
	if res.Pack.ID != "demo-pack" || len(res.Pack.Rules) != 1 {
		t.Fatalf("result=%+v", res.Pack)
	}
	if _, err := os.Stat(filepath.Join(DevicePacksDir(cfg), "demo-pack")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote device pack")
	}
}

func TestImportCommitsAndLoads(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "demo-pack", map[string]string{"hit": goodRule("Hit")})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	p, ok := cat.PackByID("demo-pack")
	if !ok || p.Source != SourceDevice || !p.Enabled {
		t.Fatalf("pack=%+v", p)
	}
}

func TestImportUsesManifestIDNotFolderName(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "weird name")
	writeMinimalPack(t, src, "my-pack", map[string]string{"hit": goodRule("Hit")})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	if _, err := os.Stat(filepath.Join(DevicePacksDir(cfg), "my-pack")); err != nil {
		testutil.FailErr(t, "stat path", err)
	}
	if _, err := os.Stat(filepath.Join(DevicePacksDir(cfg), "weird name")); !os.IsNotExist(err) {
		t.Fatal("used folder name")
	}
}

func TestImportRejectsBundledCollision(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "aws-cli", map[string]string{"hit": goodRule("Hit")})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, Replace: true})
	if !errors.Is(err, ErrPackIDCollision) {
		t.Fatalf("err=%v", err)
	}
}

func TestImportPreviewMakesBundledRuleIDConflictInert(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	base, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	var stolen string
	for _, pack := range base.Packs {
		if pack.Source == SourceDevice {
			continue
		}
		for _, rule := range pack.Rules {
			if rule.Supported {
				stolen = rule.ID
				break
			}
		}
		if stolen != "" {
			break
		}
	}
	if stolen == "" {
		t.Fatal("no bundled rule id")
	}
	src := filepath.Join(t.TempDir(), "src")
	body := strings.Replace(goodRule("Conflict"), "33333333-3333-4333-8333-333333333331", stolen, 1)
	writeMinimalPack(t, src, "conflict-pack", map[string]string{"hit": body})
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
	testutil.FailErr(t, "ImportPack", err)
	// The preview says which rule already answers to that id, so the reason is
	// actionable rather than "conflict".
	if len(res.Pack.Rules) != 1 || res.Pack.Rules[0].Supported ||
		!strings.Contains(res.Pack.Rules[0].UnsupportedReason, "already used by") {
		t.Fatalf("preview rule=%+v", res.Pack.Rules)
	}
}

func TestImportDeviceCollisionNeedsReplace(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "dev-pack", map[string]string{"hit": goodRule("Hit")})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "first import", err)
	_, err = ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	if !errors.Is(err, ErrPackIDCollision) {
		t.Fatalf("err=%v", err)
	}
	// Replace with a different rule set.
	writeMinimalPack(t, src, "dev-pack", map[string]string{"other": `title: Other
id: 44444444-4444-4444-8444-444444444444
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: otherbin}
  condition: sel
`})
	_, err = ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, Replace: true})
	testutil.FailErr(t, "replace", err)
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	p, _ := cat.PackByID("dev-pack")
	if len(p.Rules) != 1 || p.Rules[0].Slug != "other" {
		t.Fatalf("rules=%+v", p.Rules)
	}
}

func TestImportAllowlist(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "allow-pack", map[string]string{"hit": goodRule("Hit")})
	testutil.FailErr(t, "readme", os.WriteFile(filepath.Join(src, "README.md"), []byte("x"), 0o600))
	testutil.FailErr(t, "git", os.MkdirAll(filepath.Join(src, ".git"), 0o700))
	testutil.FailErr(t, "evil", os.WriteFile(filepath.Join(src, "evil.sh"), []byte("x"), 0o600))
	testutil.FailErr(t, "deep", os.MkdirAll(filepath.Join(src, "rules", "deep"), 0o700))
	testutil.FailErr(t, "deep rule", os.WriteFile(filepath.Join(src, "rules", "deep", "x.yml"), []byte("x"), 0o600))
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
	testutil.FailErr(t, "ImportPack", err)
	joined := strings.Join(res.Ignored, ",")
	for _, need := range []string{"README.md", ".git/", "evil.sh", "rules/deep/"} {
		if !strings.Contains(joined, need) {
			t.Fatalf("ignored=%v missing %s", res.Ignored, need)
		}
	}
}

func TestImportSkipsSymlinks(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "sym-pack", map[string]string{"hit": goodRule("Hit")})
	outside := filepath.Join(t.TempDir(), "outside.yml")
	testutil.FailErr(t, "outside", os.WriteFile(outside, []byte(goodRule("Out")), 0o600))
	link := filepath.Join(src, "rules", "link.yml")
	testutil.FailErr(t, "symlink", os.Symlink(outside, link))
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	found := false
	for _, i := range res.Ignored {
		if i == "rules/link.yml" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ignored=%v", res.Ignored)
	}
	if _, err := os.Stat(filepath.Join(DevicePacksDir(cfg), "sym-pack", "rules", "link.yml")); !os.IsNotExist(err) {
		t.Fatal("symlink was copied")
	}
}

func TestImportRejectsSymlinkedManifest(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	testutil.FailErr(t, "mkdir source", os.MkdirAll(filepath.Join(src, "rules"), 0o700))
	realManifest := filepath.Join(t.TempDir(), "pack.yaml")
	testutil.FailErr(t, "write manifest", os.WriteFile(realManifest, []byte(
		"id: symlink-pack\nlabel: Symlink\ndescription: Must be rejected\n",
	), 0o600))
	testutil.FailErr(t, "symlink manifest", os.Symlink(realManifest, filepath.Join(src, "pack.yaml")))
	testutil.FailErr(t, "write rule", os.WriteFile(filepath.Join(src, "rules", "hit.yml"), []byte(goodRule("Hit")), 0o600))
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(DevicePacksDir(cfg), "symlink-pack")); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked manifest must not install, stat err=%v", statErr)
	}
}

func TestImportCaps(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()

	t.Run("rule count", func(t *testing.T) {
		t.Parallel()
		src := filepath.Join(t.TempDir(), "src")
		rules := map[string]string{}
		for i := 0; i < 201; i++ {
			slug := "r" + strings.Repeat("x", 0) + itoa(i)
			rules[slug] = strings.ReplaceAll(goodRule("R"), "33333333-3333-4333-8333-333333333331", fakeUUID(i))
		}
		writeMinimalPack(t, src, "cap-rules", rules)
		_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
		if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "200 rule") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("file size", func(t *testing.T) {
		t.Parallel()
		src := filepath.Join(t.TempDir(), "src")
		writeMinimalPack(t, src, "cap-file", map[string]string{"hit": goodRule("Hit")})
		big := strings.Repeat("a", maxFileBytes+1)
		testutil.FailErr(t, "big", os.WriteFile(filepath.Join(src, "rules", "big.yml"), []byte(big), 0o600))
		_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
		if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "64 KiB") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("pack size", func(t *testing.T) {
		t.Parallel()
		src := filepath.Join(t.TempDir(), "src")
		writeMinimalPack(t, src, "cap-pack", map[string]string{"hit": goodRule("Hit")})
		// Many medium files totaling >4MiB.
		n := (maxPackBytes / (maxFileBytes / 2)) + 2
		for i := 0; i < n; i++ {
			body := strings.ReplaceAll(goodRule("R"), "33333333-3333-4333-8333-333333333331", fakeUUID(i+300))
			pad := strings.Repeat("#", maxFileBytes/2-len(body))
			testutil.FailErr(t, "pad", os.WriteFile(filepath.Join(src, "rules", "p"+itoa(i)+".yml"), []byte(body+"\n"+pad), 0o600))
		}
		_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
		if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "4 MiB") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestImportRejectsNoParsableRules(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "bad-pack", map[string]string{"hit": "not: valid: [[["})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src, DryRun: true})
	if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "zero parsable") {
		t.Fatalf("err=%v", err)
	}
}

func TestImportPartialRulesReported(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "partial-pack", map[string]string{
		"hit": goodRule("Hit"),
		"bad": "title: X\nid: not-uuid\ndescription: d\nlevel: high\nlogsource: {product: lycaon, service: tool_exec}\ndetection:\n  sel: {Image: x}\n  condition: sel\n",
	})
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	if len(res.RejectedRules) != 1 || !strings.Contains(res.RejectedRules[0].File, "bad.yml") {
		t.Fatalf("rejected=%v", res.RejectedRules)
	}
}

func TestImportUnsupportedRuleKept(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "unsup-pack", map[string]string{
		"hit": `title: Base64
id: 55555555-5555-4555-8555-555555555555
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel:
    CommandLine|base64: YQ==
  condition: sel
`,
	})
	res, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	if len(res.Pack.Rules) != 1 || res.Pack.Rules[0].Supported {
		t.Fatalf("pack rules=%+v", res.Pack.Rules)
	}
	if !strings.Contains(res.Pack.Rules[0].UnsupportedReason, "unsupported modifier") {
		t.Fatalf("reason=%q", res.Pack.Rules[0].UnsupportedReason)
	}
	r := res.Pack.Rules[0]
	if r.Matches(testEvent("command", "echo hi", "/p", true, "proxy", "s")) {
		t.Fatal("unsupported must not match")
	}
}

func TestImportAtomicOnFailure(t *testing.T) {
	// Not parallel: mutates package-level copyFileFn.
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "atomic-pack", map[string]string{"hit": goodRule("Hit")})
	old := copyFileFn
	t.Cleanup(func() { copyFileFn = old })
	copyFileFn = func(src, dst string) error {
		return errors.New("injected write failure")
	}
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	if err == nil {
		t.Fatal("expected error")
	}
	deviceRoot := DevicePacksDir(cfg)
	if ents, err := os.ReadDir(deviceRoot); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".import-") || e.Name() == "atomic-pack" {
				t.Fatalf("leftover %s", e.Name())
			}
		}
	}
}

func TestRemoveDevicePack(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	writeMinimalPack(t, src, "rm-pack", map[string]string{"hit": goodRule("Hit")})
	_, err := ImportPack(cfg, shippedPacks(t), ImportRequest{SourcePath: src})
	testutil.FailErr(t, "ImportPack", err)
	testutil.FailErr(t, "RemoveDevicePack", RemoveDevicePack(cfg, shippedPacks(t), "rm-pack"))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	if _, ok := cat.PackByID("rm-pack"); ok {
		t.Fatal("pack still present")
	}
}

func TestRemoveDevicePackRejectsSymlinkTarget(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	deviceRoot := DevicePacksDir(cfg)
	testutil.FailErr(t, "mkdir device root", os.MkdirAll(deviceRoot, 0o700))
	realPack := filepath.Join(deviceRoot, "real-pack")
	testutil.FailErr(t, "mkdir real pack", os.MkdirAll(realPack, 0o700))
	marker := filepath.Join(realPack, "keep")
	testutil.FailErr(t, "write marker", os.WriteFile(marker, []byte("keep"), 0o600))
	testutil.FailErr(t, "symlink pack", os.Symlink(realPack, filepath.Join(deviceRoot, "link-pack")))
	err := RemoveDevicePack(cfg, shippedPacks(t), "link-pack")
	if !errors.Is(err, ErrPackInvalid) || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("symlink target was altered: %v", statErr)
	}
}

func TestRemoveBundledRejected(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	err := RemoveDevicePack(cfg, shippedPacks(t), "aws-cli")
	if !errors.Is(err, ErrPackNotRemovable) {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoveTraversalRejected(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	for _, id := range []string{"../..", "/tmp/evil", "has/slash"} {
		err := RemoveDevicePack(cfg, shippedPacks(t), id)
		if err == nil {
			t.Fatalf("expected error for %q", id)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func fakeUUID(i int) string {
	// RFC 4122-ish unique hex id per index.
	hex := "0123456789abcdef"
	var s strings.Builder
	n := i
	for len := 0; len < 32; len++ {
		s.WriteByte(hex[n%16])
		n = n/16 + len + 1
	}
	raw := s.String()
	// version 4 variant bits
	return raw[0:8] + "-" + raw[8:12] + "-4" + raw[13:16] + "-8" + raw[17:20] + "-" + raw[20:32]
}
