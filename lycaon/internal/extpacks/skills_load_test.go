package extpacks

import (
	"github.com/lycaon/lycaon/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnitIDFromRelSkills(t *testing.T) {
	cases := []struct {
		rel  string
		want string
	}{
		{"skills/foo/SKILL.md", "skills/foo"},
		{"skills/foo/references/REFERENCE.md", ""},
		{"skills/foo/scripts/x.py", ""},
		{"skills/foo/sub/SKILL.md", ""},
		{"skills/SKILL.md", ""},
	}
	for _, tc := range cases {
		got := UnitIDFor("acme/kit", tc.rel)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.rel, got, tc.want)
		}
	}
}

func TestKindRootIncludesSkills(t *testing.T) {
	found := false
	for _, root := range UnitKindRoots() {
		if root == "skills" {
			found = true
		}
	}
	if !found {
		t.Fatal("UnitKindRoots must contain skills")
	}
	if kindRootForRel("skills/foo/SKILL.md") != "skills" {
		t.Fatalf("kindRootForRel = %q", kindRootForRel("skills/foo/SKILL.md"))
	}
	if ProjectScope("skills") != ScopeDeviceOnly {
		t.Fatalf("skills scope = %v want ScopeDeviceOnly", ProjectScope("skills"))
	}
}

func TestProjectScopeCoversEveryKindRoot(t *testing.T) {
	for _, root := range UnitKindRoots() {
		if ProjectScope(root) == 0 {
			t.Fatalf("kind root %q has no ProjectScope class", root)
		}
	}
}

func TestLoadEffectiveSkillsBundledResources(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "skill-pack", Manifest{
		ID: "test/skill-pack", Name: "skills",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{
		"skills/foo/SKILL.md":            "---\nname: foo\ndescription: A test skill.\n---\nDo the thing.\n",
		"skills/foo/scripts/run.sh":      "#!/bin/sh\necho hi\n",
		"skills/foo/references/R.md":     "# ref\n",
		"skills/foo/scripts/ignore.link": "", // overwritten as symlink below
	})
	packRoot := filepath.Join(dir, "skill-pack")
	link := filepath.Join(packRoot, "skills", "foo", "scripts", "ignore.link")
	_ = os.Remove(link)
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Skip("symlink not available")
	}

	pc, err := InventoryPack(Pack{ID: "test/skill-pack", Root: OnDisk(packRoot)}, Manifest{
		ID:            "test/skill-pack",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	var skillUnits int
	for _, u := range pc.Units {
		if strings.HasPrefix(u.ID, "skills/") {
			skillUnits++
		}
	}
	if skillUnits != 1 {
		t.Fatalf("inventoried skill units = %d want 1", skillUnits)
	}

	platform := mustStockPack(t, "painted-wolf/platform")
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pc},
		Desired: desiredWithExtensionPacks("test/skill-pack"),
	})
	loaded, diags := LoadEffectiveSkills(eff)
	var foo *skills.Skill
	for i := range loaded {
		if loaded[i].Name == "foo" {
			foo = &loaded[i]
			break
		}
	}
	if foo == nil {
		t.Fatalf("foo not loaded; diags=%v", diags)
	}
	if !foo.UserProvided {
		t.Fatal("non-stock installed skill must be user-provided")
	}
	if foo.Dir == "" {
		t.Fatal("Dir must be set")
	}
	if foo.Body != "Do the thing.\n" && foo.Body != "Do the thing." {
		if !strings.HasPrefix(foo.Body, "Do the thing.") {
			t.Fatalf("body = %q", foo.Body)
		}
	}
	want := []string{"references/R.md", "scripts/run.sh"}
	if len(foo.Resources) != 2 || foo.Resources[0] != want[0] || foo.Resources[1] != want[1] {
		t.Fatalf("resources = %#v want %#v (symlink must be skipped)", foo.Resources, want)
	}
	for _, d := range diags {
		if d.UnitID == "skills/foo" && d.Code == DiagSkillFrontmatterInvalid {
			t.Fatalf("unexpected diag: %+v", d)
		}
	}
}

func TestLoadEffectiveSkillsFailSoftAndCap(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 5; i++ {
		name := "s" + itoa(i)
		if i == 2 {
			files["skills/"+name+"/SKILL.md"] = "---\nname: " + name + "\n---\n" // missing description
			continue
		}
		files["skills/"+name+"/SKILL.md"] = "---\nname: " + name + "\ndescription: skill " + name + "\n---\n"
	}
	writeFixturePack(t, dir, "five", Manifest{
		ID: "test/five", Name: "five",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, files)
	pc, err := InventoryPack(Pack{ID: "test/five", Root: OnDisk(filepath.Join(dir, "five"))}, Manifest{
		ID:            "test/five",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	platform := mustStockPack(t, "painted-wolf/platform")
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{platform, pc},
		Desired: desiredWithExtensionPacks("test/five"),
	})
	loaded, diags := LoadEffectiveSkills(eff)
	var fromPack int
	for _, sk := range loaded {
		if sk.PackID == "test/five" {
			fromPack++
		}
	}
	if fromPack != 4 {
		t.Fatalf("from pack = %d want 4; diags=%v", fromPack, diags)
	}
	foundMissing := false
	for _, d := range diags {
		if d.Code == DiagSkillFieldMissing && d.UnitID == "skills/s2" {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("expected skill_field_missing for s2; diags=%v", diags)
	}
}

func TestLoadEffectiveSkillsCatalogCap(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 130; i++ {
		name := "c" + pad3(i)
		files["skills/"+name+"/SKILL.md"] = "---\nname: " + name + "\ndescription: skill " + name + "\n---\n"
	}
	writeFixturePack(t, dir, "cap", Manifest{
		ID: "test/cap", Name: "cap",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, files)
	pc, err := InventoryPack(Pack{ID: "test/cap", Root: OnDisk(filepath.Join(dir, "cap"))}, Manifest{
		ID:            "test/cap",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pc},
		Desired: desiredWithExtensionPacks("test/cap"),
	})
	loaded, diags := LoadEffectiveSkills(eff)
	if len(loaded) != skills.CatalogMax {
		t.Fatalf("loaded = %d want %d", len(loaded), skills.CatalogMax)
	}
	found := false
	for _, d := range diags {
		if d.Code == DiagSkillCatalogFull {
			found = true
			if d.Message != "The skills catalog is full at 128; skills beyond the cap were not loaded." {
				t.Fatalf("message = %q", d.Message)
			}
		}
	}
	if !found {
		t.Fatalf("expected catalog_full; diags=%v", diags)
	}
}

func pad3(i int) string {
	s := itoa(i)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func TestLoadEffectiveSkillsLayering(t *testing.T) {
	dir := t.TempDir()
	bodyA := "---\nname: foo\ndescription: from a\n---\nA\n"
	bodyB := "---\nname: foo\ndescription: from b\n---\nB\n"
	writeFixturePack(t, dir, "a", Manifest{
		ID: "test/a", Name: "a",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"skills/foo/SKILL.md": bodyA})
	writeFixturePack(t, dir, "b", Manifest{
		ID: "test/b", Name: "b",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{"skills/foo/SKILL.md": bodyB})
	pcA, err := InventoryPack(Pack{ID: "test/a", Root: OnDisk(filepath.Join(dir, "a"))}, Manifest{
		ID:            "test/a",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "inv a", err)
	pcB, err := InventoryPack(Pack{ID: "test/b", Root: OnDisk(filepath.Join(dir, "b"))}, Manifest{
		ID:            "test/b",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "inv b", err)

	conflict := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pcA, pcB},
		Desired: desiredWithExtensionPacks("test/a", "test/b"),
	})
	if conflict.HasLoaded("skills/foo") {
		t.Fatal("conflicting skills/foo must not load without own")
	}

	selected := desiredWithExtensionPacks("test/a", "test/b")
	selected.Own = map[string]string{"skills/foo": "test/b"}
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pcA, pcB},
		Desired: selected,
	})
	loaded, _ := LoadEffectiveSkills(eff)
	if len(loaded) != 1 || loaded[0].PackID != "test/b" || !strings.HasPrefix(loaded[0].Body, "B") {
		t.Fatalf("own winner = %#v", loaded)
	}

	disabled := desiredWithExtensionPacks("test/a", "test/b")
	disabled.Disabled = []string{"skills/foo"}
	disabled.Own = map[string]string{"skills/foo": "test/b"}
	eff = Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pcA, pcB},
		Desired: disabled,
	})
	loaded, _ = LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		if sk.Name == "foo" {
			t.Fatal("disabled skill must not load")
		}
	}
}

func TestLoadEffectiveSkillsStockClean(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "effective catalog resolve", err)
	loaded, diags := LoadEffectiveSkills(eff)
	want := map[string]struct {
		pack string
		ref  string
	}{
		"apply-a-structural-codemod": {pack: "painted-wolf/platform", ref: "references/structural-rewrite.md"},
		"investigate-code-history":   {pack: "painted-wolf/platform", ref: "references/history-routing.md"},
		"triage-security-findings":   {pack: "painted-wolf/scan-guidance", ref: "references/scan-triage-routing.md"},
		"verify-a-change":            {pack: "painted-wolf/platform", ref: "references/failure-triage.md"},
		"write-a-skill":              {pack: "painted-wolf/platform", ref: "references/FORMAT.md"},
		"write-extension-pack":       {pack: "painted-wolf/platform", ref: "references/pack-layout.md"},
		"write-workflow":             {pack: "painted-wolf/platform", ref: "references/workflow-contract.md"},
		"verify-visual-change":       {pack: "painted-wolf/browser", ref: "references/page-evidence.md"},
		"review-accessibility":       {pack: "painted-wolf/browser", ref: "references/review-boundary.md"},
		"write-detection-pack":       {pack: "painted-wolf/security", ref: "references/sigma-subset.md"},
	}
	byName := map[string]skills.Skill{}
	for _, sk := range loaded {
		byName[sk.Name] = sk
	}
	for name, meta := range want {
		sk, ok := byName[name]
		if !ok {
			t.Fatalf("stock skill %q missing; loaded=%d diags=%v", name, len(loaded), diags)
		}
		if sk.PackID != meta.pack {
			t.Fatalf("%s PackID = %q want %q", name, sk.PackID, meta.pack)
		}
		if sk.UserProvided {
			t.Fatalf("stock skill %q must not be marked user-provided", name)
		}
		if sk.Dir == "" {
			t.Fatalf("%s missing Dir", name)
		}
		for _, d := range diags {
			if d.UnitID == sk.UnitID {
				t.Fatalf("stock %s has diag %+v", name, d)
			}
		}
		if meta.ref == "" {
			continue
		}
		foundRef := false
		for _, r := range sk.Resources {
			if r == meta.ref {
				foundRef = true
			}
		}
		if !foundRef {
			t.Fatalf("%s resources = %#v want %q", name, sk.Resources, meta.ref)
		}
	}
}

func TestLoadEffectiveSkillsBrowserPackDisable(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/browser"), nil)
	testutil.FailErr(t, "effective catalog resolve browser off", err)
	if eff.PackContributed("painted-wolf/browser") {
		t.Fatal("browser pack must not contribute when disabled")
	}
	loaded, _ := LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		switch sk.Name {
		case "verify-visual-change", "review-accessibility":
			t.Fatalf("browser skill %q still loaded when browser pack disabled", sk.Name)
		}
		if sk.PackID == "painted-wolf/browser" {
			t.Fatalf("browser-pack skill still effective: %#v", sk)
		}
	}
	var sawPlatform bool
	for _, sk := range loaded {
		if sk.Name == "verify-a-change" || sk.Name == "write-a-skill" {
			sawPlatform = true
		}
	}
	if !sawPlatform {
		t.Fatal("platform skills must remain when browser is disabled")
	}
}

func TestLoadEffectiveSkillsAuthoringPackDisable(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), desiredWithDisabledPack("painted-wolf/security"), nil)
	testutil.FailErr(t, "effective catalog resolve security off", err)
	if eff.PackContributed("painted-wolf/security") {
		t.Fatal("security pack must not contribute when disabled")
	}
	loaded, _ := LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		if sk.Name == "write-detection-pack" || sk.PackID == "painted-wolf/security" {
			t.Fatalf("security skill still effective when pack disabled: %#v", sk)
		}
	}
	wantStill := map[string]bool{
		"write-extension-pack": false,
		"write-workflow":       false,
	}
	for _, sk := range loaded {
		if _, ok := wantStill[sk.Name]; ok {
			wantStill[sk.Name] = true
		}
	}
	for name, ok := range wantStill {
		if !ok {
			t.Fatalf("platform authoring skill %q missing after security disable", name)
		}
	}

	desired := EmptyDesired()
	desired.Disabled = []string{"skills/write-workflow"}
	effUnit, err := resolveWithDesired(t.Context(), desired, nil)
	testutil.FailErr(t, "effective catalog resolve unit disable", err)
	unitLoaded, _ := LoadEffectiveSkills(effUnit)
	for _, sk := range unitLoaded {
		if sk.Name == "write-workflow" {
			t.Fatal("write-workflow must honor ordinary unit disable")
		}
	}
	var sawExt, sawDetect bool
	for _, sk := range unitLoaded {
		switch sk.Name {
		case "write-extension-pack":
			sawExt = true
		case "write-detection-pack":
			sawDetect = true
		}
	}
	if !sawExt || !sawDetect {
		t.Fatal("other authoring skills must remain when only write-workflow is disabled")
	}
}

func TestLoadEffectiveSkillsAuthoringBoundaries(t *testing.T) {
	eff, err := resolveWithDesired(t.Context(), EmptyDesired(), nil)
	testutil.FailErr(t, "effective catalog resolve", err)
	loaded, diags := LoadEffectiveSkills(eff)
	authoring := []string{"write-extension-pack", "write-workflow", "write-detection-pack"}
	const (
		bodyMax = 16 << 10
		refMax  = 12 << 10
	)
	banned := []string{
		"allowed-tools",
		"scripts/",
		"grants approval",
		"grants access",
		"auto-activate",
		"automatically activate",
		"run this script",
	}
	byName := map[string]skills.Skill{}
	for _, sk := range loaded {
		byName[sk.Name] = sk
	}
	for _, name := range authoring {
		sk, ok := byName[name]
		if !ok {
			t.Fatalf("missing authoring skill %q; diags=%v", name, diags)
		}
		if len(sk.Resources) == 0 {
			t.Fatalf("%s resources = %#v want at least one reference", name, sk.Resources)
		}
		// Bundled paths resolve through the embedded catalog.
		bodyPath := config.Rel(sk.Dir).Join("SKILL.md")
		body, err := config.Read(bodyPath)
		testutil.FailErr(t, "read "+bodyPath.String(), err)
		if len(body) > bodyMax {
			t.Fatalf("%s body = %d bytes; ceiling %d", name, len(body), bodyMax)
		}
		combined := strings.ToLower(string(body))
		for _, resource := range sk.Resources {
			refPath := config.Rel(sk.Dir).Join(resource)
			ref, err := config.Read(refPath)
			testutil.FailErr(t, "read "+refPath.String(), err)
			if len(ref) > refMax {
				t.Fatalf("%s reference %s = %d bytes; ceiling %d", name, resource, len(ref), refMax)
			}
			combined += "\n" + strings.ToLower(string(ref))
		}
		for _, b := range banned {
			if strings.Contains(combined, strings.ToLower(b)) {
				t.Fatalf("%s must not contain %q", name, b)
			}
		}
		if sk.AllowedTools != "" {
			t.Fatalf("%s AllowedTools must be empty; got %q", name, sk.AllowedTools)
		}
		scriptsDir := filepath.Join(sk.Dir, "scripts")
		if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
			t.Fatalf("%s must not ship a scripts/ directory", name)
		}
	}
}

func TestLoadEffectiveSkillsAllowedToolsInert(t *testing.T) {
	dir := t.TempDir()
	writeFixturePack(t, dir, "tools", Manifest{
		ID: "test/tools", Name: "tools",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{
		"skills/toolish/SKILL.md": "---\nname: toolish\ndescription: retains tools\nallowed-tools: Bash(rm:*) Read\n---\n",
	})
	pc, err := InventoryPack(Pack{ID: "test/tools", Root: OnDisk(filepath.Join(dir, "tools"))}, Manifest{
		ID:            "test/tools",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pc},
		Desired: desiredWithExtensionPacks("test/tools"),
	})
	loaded, _ := LoadEffectiveSkills(eff)
	if len(loaded) != 1 || loaded[0].AllowedTools != "Bash(rm:*) Read" {
		t.Fatalf("%#v", loaded)
	}
}

func TestSkillsLoadSourceNoExec(t *testing.T) {
	data, err := os.ReadFile("skills_load.go")
	testutil.FailErr(t, "read skills_load.go", err)
	src := string(data)
	if strings.Contains(src, "os/exec") || strings.Contains(src, "exec.") {
		t.Fatal("skills_load.go must not execute")
	}
}

func TestSkillDiagnosticsRegisteredAndReachable(t *testing.T) {
	want := []string{
		DiagSkillNameInvalid,
		DiagSkillNameMismatch,
		DiagSkillFrontmatterInvalid,
		DiagSkillFieldMissing,
		DiagSkillFieldInvalid,
		DiagSkillCompatibilityInvalid,
		DiagSkillTooLarge,
		DiagSkillCatalogFull,
	}
	reg := map[string]bool{}
	for _, c := range AllDiagnosticCodes() {
		reg[c] = true
	}
	for _, c := range want {
		if !reg[c] {
			t.Fatalf("%s missing from allDiagnosticCodes", c)
		}
	}

	dir := t.TempDir()
	files := map[string]string{
		"skills/bad_name_here!!/SKILL.md": "---\nname: x\ndescription: d\n---\n", // directory name fails skills.ValidName
		"skills/mismatch/SKILL.md":        "---\nname: other\ndescription: d\n---\n",
		"skills/nofence/SKILL.md":         "nope\n",
		"skills/repair/SKILL.md":          "---\nname: repair\ndescription: Use when: needed\n---\n",
		"skills/nodesc/SKILL.md":          "---\nname: nodesc\n---\n",
		"skills/longdesc/SKILL.md":        "---\nname: longdesc\ndescription: " + strings.Repeat("a", 1025) + "\n---\n",
		"skills/longcompat/SKILL.md":      "---\nname: longcompat\ndescription: d\ncompatibility: " + strings.Repeat("c", 501) + "\n---\n",
	}
	big := strings.Repeat("x", skills.BodyMax+1)
	files["skills/toolarge/SKILL.md"] = "---\nname: toolarge\ndescription: d\n---\n" + big

	writeFixturePack(t, dir, "diags", Manifest{
		ID: "test/diags", Name: "diags",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, files)
	// Invalid skill names surface name_invalid.
	badDir := filepath.Join(dir, "diags", "skills", "BadName")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir BadName", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\nname: BadName\ndescription: d\n---\n"), 0o644); err != nil {
		testutil.FailErr(t, "write BadName", err)
	}

	pc, err := InventoryPack(Pack{ID: "test/diags", Root: OnDisk(filepath.Join(dir, "diags"))}, Manifest{
		ID:            "test/diags",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	eff := Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{pc},
		Desired: desiredWithExtensionPacks("test/diags"),
	})
	_, diags := LoadEffectiveSkills(eff)
	seen := map[string]bool{}
	for _, d := range diags {
		seen[d.Code] = true
	}
	for _, c := range want {
		if c == DiagSkillCatalogFull {
			continue
		}
		if !seen[c] {
			t.Errorf("diagnostic %s not reached; seen=%v", c, seen)
		}
	}
}

// packWithSkills resolves one fixture pack shipping the given SKILL.md bodies.
func packWithSkills(t *testing.T, bodies map[string]string) *EffectiveCatalog {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for name, body := range bodies {
		files["skills/"+name+"/SKILL.md"] = body
	}
	writeFixturePack(t, dir, "one", Manifest{
		ID: "test/one", Name: "one",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, files)
	pc, err := InventoryPack(Pack{ID: "test/one", Root: OnDisk(filepath.Join(dir, "one"))}, Manifest{
		ID:            "test/one",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	return Resolve(t.Context(), ResolveInput{
		Packs:   []PackContent{mustStockPack(t, "painted-wolf/platform"), pc},
		Desired: desiredWithExtensionPacks("test/one"),
	})
}

// A pack body is a template; an unparsable one is refused at load rather than
// at activation.
func TestPackSkillWithAnInvalidTemplateIsRefused(t *testing.T) {
	eff := packWithSkills(t, map[string]string{
		"broken": "---\nname: broken\ndescription: Body carries an unclosed tag.\n---\n\nDepth is {{ max_tool_loops.\n",
	})
	loaded, diags := LoadEffectiveSkills(eff)
	for _, sk := range loaded {
		if sk.Name == "broken" {
			t.Fatal("a skill whose body will not render must not load")
		}
	}
	if !hasDiag(diags, DiagSkillTemplateInvalid, "skills/broken") {
		t.Fatalf("want skill_template_invalid for skills/broken; diags=%v", diags)
	}
}

// Skill refusals remain visible in catalog diagnostics.
func TestSkillDiagnosticsReachTheCatalog(t *testing.T) {
	eff := packWithSkills(t, map[string]string{
		"stated": "---\nname: a-different-name\ndescription: Frontmatter disagrees with its folder.\n---\n\nBody.\n",
	})
	if !hasDiag(eff.Diagnostics, DiagSkillNameMismatch, "skills/stated") {
		t.Fatalf("want skill_name_mismatch on the catalog; diags=%v", eff.Diagnostics)
	}
}

func hasDiag(diags []Diagnostic, code, unitID string) bool {
	for _, d := range diags {
		if d.Code == code && d.UnitID == unitID {
			return true
		}
	}
	return false
}
