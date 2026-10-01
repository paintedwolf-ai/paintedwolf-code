package extpacks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMatchingMetaReleaseRefsBoundsMaterializationAfterConstraintFiltering(t *testing.T) {
	refs := make([]string, 0, maxMaterializedReleaseCandidates+2)
	for version := maxMaterializedReleaseCandidates + 2; version > 0; version-- {
		refs = append(refs, fmt.Sprintf("v1.0.%d", version))
	}
	want, err := semver.NewConstraint("<=1.0.2")
	testutil.FailErr(t, "parse constraint", err)
	matching, truncated := matchingMetaReleaseRefs(refs, want)
	if truncated || len(matching) != 2 || matching[0] != "v1.0.2" || matching[1] != "v1.0.1" {
		t.Fatalf("matching=%v truncated=%t", matching, truncated)
	}

	want, err = semver.NewConstraint("*")
	testutil.FailErr(t, "parse constraint", err)
	matching, truncated = matchingMetaReleaseRefs(refs, want)
	if !truncated || len(matching) != maxMaterializedReleaseCandidates {
		t.Fatalf("matching count=%d truncated=%t", len(matching), truncated)
	}
}

func TestCopyDirRejectsSymlinksBeforeReadingTheirTargets(t *testing.T) {
	source := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	testutil.FailErr(t, "write secret", os.WriteFile(secret, []byte("do-not-copy"), 0o600))
	testutil.FailErr(t, "create symlink", os.Symlink(secret, filepath.Join(source, "linked-secret")))
	destination := filepath.Join(t.TempDir(), "copy")

	err := copyDir(source, destination)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("copyDir error = %v, want non-regular-file rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "linked-secret")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was copied: %v", statErr)
	}
}

func TestStockMetaMembersMatchDiscover(t *testing.T) {
	metas, diags, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	for _, d := range diags {
		if d.Code == DiagMetaInvalid || d.Code == DiagMetaCompatibility {
			t.Fatalf("unexpected stock meta diagnostic: %+v", d)
		}
	}
	var stock *MetaPack
	for i := range metas {
		if IsStockMetaPackID(metas[i].Manifest.ID) {
			stock = &metas[i]
			break
		}
	}
	if stock == nil {
		t.Fatal("expected painted-wolf/stock meta-pack")
	}
	packs, err := DiscoverStock()
	testutil.FailErr(t, "DiscoverStock", err)
	want := map[string]struct{}{}
	for _, p := range packs {
		want[p.ID] = struct{}{}
	}
	got := map[string]struct{}{}
	for _, id := range stock.Manifest.Members {
		got[id] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("members=%d DiscoverStock=%d", len(got), len(want))
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("stock meta missing member %q", id)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Fatalf("stock meta extra member %q", id)
		}
	}
}

func TestSuiteStatusCompletePartialInactive(t *testing.T) {
	members := []string{"a/x", "a/y", "a/z"}
	present := map[string]bool{"a/x": true, "a/y": true, "a/z": true}
	enabled := map[string]bool{"a/x": true, "a/y": true, "a/z": true}

	st, diags := SuiteStatus(members, present, enabled)
	if st != MetaPackComplete || len(diags) != 0 {
		t.Fatalf("complete: status=%s diags=%v", st, diags)
	}

	enabled["a/y"] = false
	st, diags = SuiteStatus(members, present, enabled)
	if st != MetaPackPartial {
		t.Fatalf("partial want partial, got %s", st)
	}
	if !hasDiagCode(diags, DiagMemberDisabled, "a/y") {
		t.Fatalf("expected member_disabled for a/y: %v", diags)
	}

	enabled = map[string]bool{"a/x": false, "a/y": false, "a/z": false}
	st, _ = SuiteStatus(members, present, enabled)
	if st != MetaPackInactive {
		t.Fatalf("inactive want inactive, got %s", st)
	}

	present = map[string]bool{"a/x": true}
	enabled = map[string]bool{"a/x": true}
	st, diags = SuiteStatus(members, present, enabled)
	if st != MetaPackPartial {
		t.Fatalf("missing+enabled want partial, got %s", st)
	}
	if !hasDiagCode(diags, DiagMemberMissing, "a/y") {
		t.Fatalf("expected member_missing: %v", diags)
	}

	present = map[string]bool{}
	enabled = map[string]bool{}
	st, _ = SuiteStatus(members, present, enabled)
	if st != MetaPackInactive {
		t.Fatalf("all missing want inactive, got %s", st)
	}
}

func TestSuiteConflictSymmetric(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
		"pw/b": stockUnits("pw/b"),
	}, MetaPackManifest{
		ID: StockMetaPackID, Name: "Stock", Version: "1.0.0",
		Members: []string{"pw/a", "pw/b"},
	})

	suiteRoot := t.TempDir()
	writeMetaYAML(t, filepath.Join(suiteRoot, MetaPackFileName), MetaPackManifest{
		ID: "acme/alt", Name: "Alt", Version: "1.0.0",
		Members:       []string{"pw/a"},
		ConflictsWith: []string{StockMetaPackID},
	})
	installMetaForTest(t, "path:"+suiteRoot)

	content, err := DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	desired := desiredWithExtensionPacks("pw/a", "pw/b", "acme/alt")
	eff := Resolve(t.Context(), ResolveInput{Packs: content, Desired: desired})
	metas, _, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	sums := BuildMetaPackSummaries(eff, metas, desired)
	if CountSuiteConflictMetas(sums) < 2 {
		t.Fatalf("expected both suites to carry suite_conflict, got %+v", sums)
	}

	// A suite conflict reaches the report as an error-severity diagnostic, which
	// fails the gate.
	rep := &ValidateReport{Packs: eff.Packs}
	testutil.FailErr(t, "ApplyMetaValidate", ApplyMetaValidate(t.Context(), rep, eff, content))
	if rep.SuiteConflicts < 2 {
		t.Fatalf("want suite_conflicts>=2, got %d", rep.SuiteConflicts)
	}
	conflicts := 0
	for _, d := range rep.Diagnostics {
		if d.Code == DiagSuiteConflict {
			conflicts++
		}
	}
	if conflicts < 2 || !HasErrorDiagnostic(rep.Diagnostics) {
		t.Fatalf("suite conflicts must fail the gate: %d diagnostics %+v", conflicts, rep.Diagnostics)
	}
}

func TestSuiteConflictInactiveNoFire(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
		"pw/b": stockUnits("pw/b"),
	}, MetaPackManifest{
		ID: StockMetaPackID, Name: "Stock", Version: "1.0.0",
		Members: []string{"pw/a", "pw/b"},
	})
	suiteRoot := t.TempDir()
	writeMetaYAML(t, filepath.Join(suiteRoot, MetaPackFileName), MetaPackManifest{
		ID: "acme/alt", Name: "Alt", Version: "1.0.0",
		Members:       []string{"pw/a"},
		ConflictsWith: []string{StockMetaPackID},
	})
	installMetaForTest(t, "path:"+suiteRoot)

	content, err := DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	desired := desiredWithDisabledPack("pw/a")
	eff := Resolve(t.Context(), ResolveInput{Packs: content, Desired: desired})
	metas, _, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	sums := BuildMetaPackSummaries(eff, metas, desired)
	if CountSuiteConflictMetas(sums) != 0 {
		t.Fatalf("inactive peer must not fire suite_conflict: %+v", sums)
	}
}

func TestExtendsCoResolveConflict(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	stageStockNamespace(t, map[string]map[string]string{
		"pw/parent-leaf": {"guidance/shared-unit.md": "# parent\n"},
	}, MetaPackManifest{
		ID: StockMetaPackID, Name: "Stock", Version: "1.0.0",
		Members: []string{"pw/parent-leaf"},
	})

	suiteRoot := t.TempDir()
	writeFixturePack(t, suiteRoot, "child-leaf", Manifest{
		ID: "acme/child-leaf", Name: "Child", Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	}, map[string]string{
		"guidance/shared-unit.md": "# child\n",
	})
	writeMetaYAML(t, filepath.Join(suiteRoot, MetaPackFileName), MetaPackManifest{
		ID: "acme/kit", Name: "Kit", Version: "1.0.0",
		Members: []string{"acme/child-leaf"},
		Extends: []string{StockMetaPackID},
	})
	installMetaForTest(t, "path:"+suiteRoot)

	content, err := DiscoverAllContent(nil)
	testutil.FailErr(t, "DiscoverAllContentFor", err)
	desired, _, err := LoadMergedDesired(nil)
	testutil.FailErr(t, "LoadMergedDesired", err)
	eff := Resolve(t.Context(), ResolveInput{Packs: content, Desired: desired})
	rep := &ValidateReport{Packs: eff.Packs}
	testutil.FailErr(t, "ApplyMetaValidate", ApplyMetaValidate(t.Context(), rep, eff, content))
	found := false
	for _, m := range rep.MetaPacks {
		if m.ID != "acme/kit" {
			continue
		}
		for _, d := range m.Diagnostics {
			if d.Code == DiagExtendsCoResolve {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected extends_co_resolve on the suite summary: %+v", rep.MetaPacks)
	}
	if !HasErrorDiagnostic(rep.Diagnostics) {
		t.Fatalf("extends_co_resolve must fail the gate: %+v", rep.Diagnostics)
	}
}

func TestMetaExtensionAPIIncompatibleOmitted(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	metaRoot := filepath.Join(cfg, enginepaths.ExtensionsMetaDirName, PackIDSafe("acme/bad-epoch"))
	body := `manifest_version: 1
id: acme/bad-epoch
name: Bad
version: "1.0.0"
compatibility:
  extension_api: "^99.0.0"
members:
  - acme/x
`
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(metaRoot, MetaPackFileName), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	metas, diags, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	for _, m := range metas {
		if m.Manifest.ID == "acme/bad-epoch" {
			t.Fatal("extension API incompatible meta-pack must be omitted")
		}
	}
	found := false
	for _, d := range diags {
		if d.Code == DiagMetaCompatibility {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected meta_compatibility diagnostic, got %v", diags)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	metaRoot := filepath.Join(cfg, enginepaths.ExtensionsMetaDirName, PackIDSafe("acme/extra"))
	body := `manifest_version: 1
id: acme/extra
name: Extra
version: "1.0.0"
compatibility:
  extension_api: "^1.0.0"
members:
  - acme/x
enable: true
`
	if err := os.MkdirAll(metaRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(metaRoot, MetaPackFileName), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	metas, diags, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	for _, m := range metas {
		if m.Manifest.ID == "acme/extra" {
			t.Fatal("unknown field must omit meta-pack")
		}
	}
	found := false
	for _, d := range diags {
		if d.Code == DiagMetaInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected meta_invalid, got %v", diags)
	}
}

func TestMetaManifestRejectsMultipleDocuments(t *testing.T) {
	body := []byte("manifest_version: 1\nid: acme/kit\nname: Kit\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\nmembers: [acme/x]\n---\nid: acme/other\n")
	_, diags := parseMetaPackManifest(body, "meta.yaml")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "multiple YAML documents") {
		t.Fatalf("diags=%v", diags)
	}
}

func TestMetaManifestRejectsDuplicateRelationships(t *testing.T) {
	base := MetaPackManifest{
		ManifestVersion: ManifestVersion,
		ID:              "acme/kit", Name: "Kit", Version: "1.0.0",
		Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		Members:       []string{"acme/x"},
	}
	tests := []struct {
		name string
		edit func(*MetaPackManifest)
		want string
	}{
		{name: "conflicts", edit: func(man *MetaPackManifest) { man.ConflictsWith = []string{"acme/other", "acme/other"} }, want: "duplicate conflicts_with"},
		{name: "extends", edit: func(man *MetaPackManifest) { man.Extends = []string{"acme/base", "acme/base"} }, want: "duplicate extends"},
		{name: "contradiction", edit: func(man *MetaPackManifest) {
			man.ConflictsWith = []string{"acme/base"}
			man.Extends = []string{"acme/base"}
		}, want: "both extended and conflicting"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			man := base
			test.edit(&man)
			if err := validateMetaPackManifest(&man); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDiscoverMetaPacksKeepsInvalidCacheEntry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	metaRoot := filepath.Join(cfg, enginepaths.ExtensionsMetaDirName, PackIDSafe("acme/incomplete"))
	writeMetaYAML(t, filepath.Join(metaRoot, MetaPackFileName), MetaPackManifest{
		ID: "acme/incomplete", Name: "Incomplete", Version: "1.0.0",
		Members: []string{"acme/x"},
	})
	_, diags, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	found := false
	for _, diag := range diags {
		found = found || diag.Code == DiagMetaInvalid && diag.PackID == "acme/incomplete"
	}
	if !found {
		t.Fatalf("expected invalid cache diagnostic, got %v", diags)
	}
	if _, err := os.Stat(metaRoot); err != nil {
		t.Fatalf("discovery modified invalid cache entry: %v", err)
	}
}

func TestStockNotInCache(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	metas, _, err := DiscoverMetaPacks()
	testutil.FailErr(t, "DiscoverMetaPacks", err)
	if len(metas) == 0 || !IsStockMetaPackID(metas[0].Manifest.ID) {
		t.Fatalf("stock must discover without cache: %+v", metas)
	}
}

func TestAttachMetaPackIDs(t *testing.T) {
	metas := []MetaPack{{
		Manifest: MetaPackManifest{ID: "suite/a", Members: []string{"p/1", "p/2"}},
	}, {
		Manifest: MetaPackManifest{ID: "suite/b", Members: []string{"p/2"}},
	}}
	packs := []PackSummary{{ID: "p/1"}, {ID: "p/2"}, {ID: "p/3"}}
	out := AttachMetaPackIDs(packs, metas)
	if strings.Join(out[0].MetaPackIDs, ",") != "suite/a" {
		t.Fatalf("p/1 ids=%v", out[0].MetaPackIDs)
	}
	if strings.Join(out[1].MetaPackIDs, ",") != "suite/a,suite/b" {
		t.Fatalf("p/2 ids=%v", out[1].MetaPackIDs)
	}
	if len(out[2].MetaPackIDs) != 0 {
		t.Fatalf("p/3 should be empty, got %v", out[2].MetaPackIDs)
	}
}

func TestValidateStockSuiteComplete(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	eff, err := ResolveCatalog(t.Context(), nil, nil)
	testutil.FailErr(t, "ResolveCatalog", err)
	rep, err := Validate(t.Context(), "", eff)
	testutil.FailErr(t, "Validate", err)
	if !rep.OK {
		t.Fatalf("stock validate want OK: conflicts=%d suite=%d diags=%v",
			rep.Conflicts, rep.SuiteConflicts, rep.Diagnostics)
	}
	if rep.SuiteConflicts != 0 {
		t.Fatalf("suite_conflicts=%d", rep.SuiteConflicts)
	}
	var stock *MetaPackSummary
	for i := range rep.MetaPacks {
		if IsStockMetaPackID(rep.MetaPacks[i].ID) {
			stock = &rep.MetaPacks[i]
			break
		}
	}
	if stock == nil || stock.Status != MetaPackComplete {
		t.Fatalf("stock status want complete: %+v", stock)
	}
	for _, p := range rep.Packs {
		if !strings.HasPrefix(p.ID, StockPackIDPrefix) {
			continue
		}
		found := false
		for _, id := range p.MetaPackIDs {
			if id == StockMetaPackID {
				found = true
			}
		}
		if !found {
			t.Fatalf("pack %s missing meta_pack_ids stock", p.ID)
		}
	}
	text := FormatValidateText(rep)
	if !strings.Contains(text, "suite_conflicts=0") {
		t.Fatalf("formatter missing suite_conflicts: %s", text)
	}
}

func stageStockNamespace(t *testing.T, packs map[string]map[string]string, meta MetaPackManifest) {
	t.Helper()
	files := map[config.Rel]string{config.StockMeta: metaYAML(meta)}
	for id, units := range packs {
		leaf := id[strings.LastIndex(id, "/")+1:]
		root := config.StockPacks.Join(leaf)
		files[root.Join(config.PackManifestName)] = string(mustYAML(t, Manifest{
			ID: id, Name: id, Compatibility: ManifestCompatibility{ExtensionAPI: "^1.0.0"},
		}))
		for rel, body := range units {
			files[root.Join(strings.Split(rel, "/")...)] = body
		}
	}
	configtest.Only(t, files)
}

func stockUnits(id string) map[string]string {
	leaf := id[strings.LastIndex(id, "/")+1:]
	return map[string]string{"guidance/" + leaf + ".md": "# " + leaf + "\n"}
}

func writeMetaYAML(t *testing.T, path string, man MetaPackManifest) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(path, []byte(metaYAML(man)), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	root := filepath.Dir(path)
	if filepath.Base(root) == PackIDSafe(man.ID) {
		testutil.FailErr(t, "write suite metadata", WriteMetaPackMetadata(root, MetaPackMetadata{
			MetaPackID: man.ID,
			Source:     root,
			Kind:       PackKindGit,
		}))
	}
}

func metaYAML(man MetaPackManifest) string {
	var b strings.Builder
	b.WriteString("manifest_version: 1\n")
	b.WriteString("id: " + man.ID + "\n")
	b.WriteString("name: " + man.Name + "\n")
	b.WriteString("version: \"" + man.Version + "\"\n")
	api := man.Compatibility.ExtensionAPI
	if api == "" {
		api = "^1.0.0"
	}
	b.WriteString("compatibility:\n  extension_api: \"" + api + "\"\n")
	b.WriteString("members:\n")
	for _, m := range man.Members {
		b.WriteString("  - " + m + "\n")
	}
	if len(man.ConflictsWith) > 0 {
		b.WriteString("conflicts_with:\n")
		for _, c := range man.ConflictsWith {
			b.WriteString("  - " + c + "\n")
		}
	} else {
		b.WriteString("conflicts_with: []\n")
	}
	if len(man.Extends) > 0 {
		b.WriteString("extends:\n")
		for _, e := range man.Extends {
			b.WriteString("  - " + e + "\n")
		}
	} else {
		b.WriteString("extends: []\n")
	}
	return b.String()
}

// installMetaForTest bypasses subsystem-owner validation.
func installMetaForTest(t *testing.T, source string) {
	t.Helper()
	plan, err := PrepareInstallMeta(t.Context(), InstallMetaOptions{Source: source})
	testutil.FailErr(t, "prepare install meta", err)
	defer plan.Close()

	desiredPath, err := DeviceDesiredPath()
	testutil.FailErr(t, "device desired path", err)
	lockPath, err := DeviceLockPath()
	testutil.FailErr(t, "device lock path", err)
	desired := EmptyDesired()
	if _, statErr := os.Stat(desiredPath); statErr == nil {
		desired, err = LoadDesiredFile(desiredPath)
		testutil.FailErr(t, "load device desired", err)
	}
	lock := EmptyLock()
	if _, statErr := os.Stat(lockPath); statErr == nil {
		lock, err = LoadLockFile(lockPath)
		testutil.FailErr(t, "load device lock", err)
	}
	desired, lock, err = plan.Apply(desired, lock)
	testutil.FailErr(t, "apply meta plan", err)
	desiredData, err := EncodeDesired(desired)
	testutil.FailErr(t, "encode desired", err)
	lockData, err := EncodeLock(lock)
	testutil.FailErr(t, "encode lock", err)
	testutil.FailErr(t, "bind meta cache root", plan.BindCacheRoots(desiredPath, lockPath, desiredData, lockData))
	testutil.FailErr(t, "publish meta cache root", plan.PublishCacheRoots())
	testutil.FailErr(t, "write desired", os.WriteFile(desiredPath, desiredData, 0o600))
	testutil.FailErr(t, "write lock", os.WriteFile(lockPath, lockData, 0o600))
	testutil.FailErr(t, "finalize meta cache root", plan.FinalizeCacheRoots())
}
