package promptrender_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
	"github.com/lycaon/lycaon/test/contract/internal/oarproducers"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

// buildDisposablePackCatalog registers an on-disk extension pack in t.TempDir()
// through normal extpacks.InventoryPack + extpacks.Resolve alongside stock packs.
func buildDisposablePackCatalog(t *testing.T, packID string, files map[string]string, own map[string]string) *extpacks.EffectiveCatalog {
	t.Helper()
	packDir := filepath.Join(t.TempDir(), "pack")
	for rel, body := range files {
		full := filepath.Join(packDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	man := extpacks.Manifest{
		ManifestVersion: 1,
		ID:              packID,
		Name:            "Disposable Audit Pack",
		Version:         "0.1.0",
		Compatibility:   extpacks.ManifestCompatibility{ExtensionAPI: "^1.2.0"},
		Feature:         "disposable-audit",
	}
	extYAML := "manifest_version: 1\nid: " + packID + "\nname: Disposable Audit Pack\nversion: 0.1.0\ncompatibility:\n  extension_api: \"^1.2.0\"\nfeature: disposable-audit\n"
	if err := os.WriteFile(filepath.Join(packDir, "extension.yaml"), []byte(extYAML), 0o644); err != nil {
		t.Fatalf("write extension.yaml: %v", err)
	}
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: packID, Root: extpacks.OnDisk(packDir)}, man)
	if err != nil {
		t.Fatalf("InventoryPack: %v", err)
	}
	pc.Kind = extpacks.PackKindPath
	stockContent, err := extpacks.DiscoverStockContent()
	if err != nil {
		t.Fatalf("DiscoverStockContent: %v", err)
	}
	enabled := true
	desired := extpacks.DesiredState{
		Format: 1,
		Packs:  []extpacks.DesiredPack{{ID: packID, Source: packDir, Enabled: &enabled}},
		Own:    own,
	}
	return extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   append(slices.Clone(stockContent), pc),
		Desired: desired,
	})
}

// TestSingleSourceDisposableCatalogAutoDiscoversNewAndChangedContributions proves
// Single-Source Acceptance: introducing new contributions (guidance, skill, OAR
// rule) and changing an existing guard + tool schema in a disposable catalog are
// discovered automatically by unchanged systemic checks, catching a planted
// invalid claim and passing the valid control.
func TestSingleSourceDisposableCatalogAutoDiscoversNewAndChangedContributions(t *testing.T) {
	s := mustStock(t)
	packID := "acme/disposable-audit"

	// Valid control + changed existing guard (adding has_any_git_root to command-jobs.md) + narrowed schema.
	validGuidance := "{% if profile_has_recall and has_git %}Inspect prior observations via `recall(query=\"auth\")`.{% endif %}\n"
	validSkill := "---\nname: disposable-audit-skill\ndescription: Audit workspace safely.\n---\n{% if profile_has_recall %}Search prior results via `recall(query=\"mod\")`.{% endif %}\n"
	validPolicy := `spec_version: "1.0"
id: DISPOSABLE_REJECT_PROBE
kind: policy
on: tool.rejected
selector: {}
requires_facts: [paintedwolf.rejection_code]
when: tool == "recall" && paintedwolf.rejection_code == "DISPOSABLE_REJECT_PROBE"
effect: block
x-paintedwolf-category: recoverable
x-paintedwolf-emit: guard:recall
message: Disposable probe rejection
what: Probe rejected recall.
cause: Synthetic probe rejection.
why: Verifies automatic single-source OAR rule discovery.
fix: Follow host next steps.
`
	validNextSteps := `steps:
  - tool: "recall"
    say: "Retry searching earlier observations with {{ call }}."
    otherwise: "Recall is unavailable on this surface."
`
	origJobsRaw, _, ok := s.Catalog.UnitContent("guidance/command-jobs")
	if !ok {
		t.Fatal("missing stock guidance/command-jobs")
	}
	changedJobs := strings.Replace(
		string(origJobsRaw),
		"{% if has_running %}",
		"{% if has_running and (has_any_git_root or not has_any_git_root) %}",
		1,
	)

	effValid := buildDisposablePackCatalog(t, packID, map[string]string{
		"guidance/disposable-probe.md":                 validGuidance,
		"skills/disposable-audit-skill/SKILL.md":       validSkill,
		"policy/DISPOSABLE_REJECT_PROBE.yaml":          validPolicy,
		"host/next-steps/DISPOSABLE_REJECT_PROBE.yaml": validNextSteps,
		"guidance/command-jobs.md":                     changedJobs,
	}, map[string]string{
		"guidance/command-jobs": packID,
	})
	if extpacks.HasErrorDiagnostic(effValid.Diagnostics) {
		t.Fatalf("disposable valid catalog had error diagnostics: %+v", effValid.Diagnostics)
	}

	// 1. Unchanged interaction inventory discovers all 3 new contributions + updated command-jobs dependency!
	baseLedger, err := interactions.BuildInteractionLedger(s.Catalog, s.Schemas, s.Names, nil)
	if err != nil {
		t.Fatalf("base BuildInteractionLedger: %v", err)
	}
	dispLedger, err := interactions.BuildInteractionLedger(effValid, s.Schemas, s.Names, nil)
	if err != nil {
		t.Fatalf("disposable BuildInteractionLedger: %v", err)
	}
	hasContrib := func(l *interactions.InteractionLedger, cid string) bool {
		return slices.ContainsFunc(l.DiscoveredInstructions, func(st interactions.InstructionSite) bool {
			return st.ContributionID == cid
		})
	}
	for _, cid := range []string{
		"guidance/disposable-probe",
		"skills/disposable-audit-skill/SKILL.md",
		"policy/DISPOSABLE_REJECT_PROBE#next-steps",
	} {
		if hasContrib(baseLedger, cid) {
			t.Fatalf("base catalog unexpectedly contained %s", cid)
		}
		if !hasContrib(dispLedger, cid) {
			t.Fatalf("unchanged BuildInteractionLedger failed to auto-discover %s", cid)
		}
	}
	hasGitDepInJobs := slices.ContainsFunc(dispLedger.DiscoveredInstructions, func(st interactions.InstructionSite) bool {
		return st.ContributionID == "guidance/command-jobs" && slices.Contains(st.CapabilityFacts, "has_any_git_root")
	})
	if !hasGitDepInJobs {
		t.Fatal("unchanged BuildInteractionLedger failed to pick up changed guard has_any_git_root on guidance/command-jobs")
	}

	// 2. Unchanged OAR producer harness discovers and fires DISPOSABLE_REJECT_PROBE without rule-ID edits!
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	if err != nil {
		t.Fatalf("CoordinatorStates: %v", err)
	}
	buildSt := pickCoordState(t, coordStates, func(c hoststate.CoordinatorState) bool {
		return c.SessionMode == api.SessionModeAgent && c.Posture == api.SessionPostureBuild &&
			c.TurnProfile.SurfaceID == "implement_investigate" && c.Loading == hoststate.LoadingAllLoaded && c.RootCount == 1
	})
	buildAuth, _ := hoststate.FreezeTurnAuthority(t.Context(), buildSt)
	dispEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: configlayout.FindModuleRoot(),
		Catalog:    effValid,
	})
	oarH, err := oarproducers.NewHarness(effValid, s.Schemas, dispEngine)
	if err != nil {
		t.Fatalf("oarproducers.NewHarness: %v", err)
	}
	outcomes, err := oarH.RunToolRejectedProducers(t.Context(), []oarproducers.AuthCell{{
		Label: buildSt.ID, Role: "coordinator", SurfaceID: buildSt.TurnProfile.SurfaceID,
		ProfileID: "coordinator", Mode: buildSt.SessionMode, Posture: buildSt.Posture,
		Auth: buildAuth, Reach: buildAuth.Reach,
	}})
	if err != nil {
		t.Fatalf("RunToolRejectedProducers: %v", err)
	}
	firedProbe := slices.ContainsFunc(outcomes, func(o oarproducers.FiredOutcome) bool {
		return strings.HasSuffix(o.RuleID, "DISPOSABLE_REJECT_PROBE")
	})
	if !firedProbe {
		t.Fatalf("unchanged RunToolRejectedProducers did not fire new rule DISPOSABLE_REJECT_PROBE; outcomes=%d", len(outcomes))
	}
	for _, o := range outcomes {
		if strings.HasSuffix(o.RuleID, "DISPOSABLE_REJECT_PROBE") && o.CheckClaims {
			if viols := promptrender.EvaluateClaimsForTest(s, "oar/"+o.RuleID, o.FixCopy, o.Cell.Auth, o.Cell.Mode, o.Cell.Posture, toolvocabtest.SubjectSelfNow(), false); len(viols) != 0 {
				t.Fatalf("valid control DISPOSABLE_REJECT_PROBE failed claim evaluation: %v", viols)
			}
		}
	}

	// 3. Planted invalid cases: undeclared capability fact in disposable pack + narrowed-out schema field on valid control!
	validNotes, vErr := prompts.CheckTemplateVocabulary(effValid)
	if vErr != nil || len(validNotes) != 0 {
		t.Fatalf("valid disposable catalog failed CheckTemplateVocabulary: err=%v notes=%v", vErr, validNotes)
	}
	effBadVocab := buildDisposablePackCatalog(t, packID, map[string]string{
		"guidance/disposable-probe.md": "{% if profile_has_nonexistent_disposable_tool %}Call `read(path=\"README.md\")`.{% endif %}\n",
	}, nil)
	badNotes, badErr := prompts.CheckTemplateVocabulary(effBadVocab)
	if badErr == nil && len(badNotes) == 0 {
		t.Fatal("expected CheckTemplateVocabulary to diagnose planted undeclared capability fact")
	}

	// Schema change behind existing contribution: narrow `recall` schema to drop `history`,
	// then verify unchanged claim evaluator catches `recall(query="a", history=true)`
	// while `recall(query="a")` passes.
	narrowedAuth := buildAuth
	clonedSchemas := make(map[string]map[string]any, len(buildAuth.Schemas))
	for k, v := range buildAuth.Schemas {
		clonedSchemas[k] = v
	}
	clonedSchemas["recall"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
	}
	narrowedAuth.Schemas = clonedSchemas
	narrowedAuth.Reach = narrowedAuth.Reach.WithSchemas(clonedSchemas)

	if okV := promptrender.EvaluateClaimsForTest(s, "guidance/disposable-probe.md ("+buildSt.ID+")", `Inspect via `+"`recall(query=\"auth\")`"+`.`, narrowedAuth, buildSt.SessionMode, buildSt.Posture, toolvocabtest.SubjectSelfNow(), true); len(okV) != 0 {
		t.Fatalf("expected valid control after schema narrowing to pass, got %v", okV)
	}
	badClaimV := promptrender.EvaluateClaimsForTest(s, "guidance/disposable-probe.md ("+buildSt.ID+")", `Inspect via `+"`recall(query=\"auth\", history=true)`"+`.`, narrowedAuth, buildSt.SessionMode, buildSt.Posture, toolvocabtest.SubjectSelfNow(), true)
	if len(badClaimV) == 0 || !strings.Contains(badClaimV[0], "history") {
		t.Fatalf("expected unchanged claim evaluator to catch schema-narrowed field history, got %v", badClaimV)
	}
}

// TestSourceDerivedInteractionFixtureRegeneration proves CT05 §2c: mutating a
// guard inside an included fragment AND mutating a helper read behind a derived
// fact in Go producer source updates the discovered PrimitiveInputs automatically
// without hand-editing any interaction list.
func TestSourceDerivedInteractionFixtureRegeneration(t *testing.T) {
	s := mustStock(t)
	goDir := filepath.Join(t.TempDir(), "goproducer")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatalf("mkdir goproducer: %v", err)
	}
	goMod := "module example.com/fixtureprod\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	writeProducerGo := func(helperBody string) *interactions.ProducerGraph {
		src := "package fixtureprod\n\ntype HostObs struct {\n\tPrimaryGit bool\n\tApprovalStrict bool\n}\n\n" +
			"func computeDerivedGate(obs HostObs) bool {\n\t" + helperBody + "\n}\n\n" +
			"func BuildVars(obs HostObs) map[string]any {\n\tm := map[string]any{}\n\tm[\"custom_derived_gate\"] = computeDerivedGate(obs)\n\treturn m\n}\n"
		if err := os.WriteFile(filepath.Join(goDir, "producer.go"), []byte(src), 0o644); err != nil {
			t.Fatalf("write producer.go: %v", err)
		}
		pg, err := interactions.LoadProducerGraphFromDir(goDir, "./...")
		if err != nil {
			t.Fatalf("LoadProducerGraphFromDir: %v", err)
		}
		return pg
	}

	packID := "acme/regen-fixture"
	runDiscovery := func(partialBody string, pg *interactions.ProducerGraph) interactions.InstructionSite {
		eff := buildDisposablePackCatalog(t, packID, map[string]string{
			"guidance/regen-host.md":        "{% include \"partials/regen-frag.md\" %}\n",
			"shared/partials/regen-frag.md": partialBody,
		}, nil)
		ledger, err := interactions.BuildInteractionLedger(eff, s.Schemas, s.Names, pg)
		if err != nil {
			t.Fatalf("BuildInteractionLedger: %v", err)
		}
		for _, st := range ledger.DiscoveredInstructions {
			if st.DeliverySite == "guidance:regen-host" && st.Action == "recall" {
				return st
			}
		}
		t.Fatalf("missing discovered site for guidance:regen-host -> partials/regen-frag.md")
		return interactions.InstructionSite{}
	}

	// Version 1: included partial guards only on profile_has_recall; Go helper reads obs.PrimaryGit.
	pgV1 := writeProducerGo("return obs.PrimaryGit")
	siteV1 := runDiscovery("{% if profile_has_recall %}Inspect with `recall(query=\"auth\")`.{% endif %}\n", pgV1)
	if len(siteV1.IncludeAncestry) == 0 || !strings.Contains(siteV1.IncludeAncestry[0], "shared/partials/regen-frag") {
		t.Fatalf("expected include ancestry through shared/partials/regen-frag, got %v", siteV1.IncludeAncestry)
	}
	if slices.Contains(siteV1.OccurrenceFacts, "custom_derived_gate") || slices.Contains(siteV1.PrimitiveInputs, "HostObs.PrimaryGit") {
		t.Fatalf("unexpected custom_derived_gate in v1 before guard edit: occ=%v prim=%v", siteV1.OccurrenceFacts, siteV1.PrimitiveInputs)
	}

	// Version 2a: mutate guard INSIDE included fragment to reference custom_derived_gate
	// (helper still reads obs.PrimaryGit).
	siteV2a := runDiscovery("{% if profile_has_recall and custom_derived_gate %}Inspect with `recall(query=\"auth\")`.{% endif %}\n", pgV1)
	if !slices.Contains(siteV2a.OccurrenceFacts, "custom_derived_gate") {
		t.Fatalf("expected regenerated fragment guard to include custom_derived_gate, got %v", siteV2a.OccurrenceFacts)
	}
	if !slices.Contains(siteV2a.PrimitiveInputs, "HostObs.PrimaryGit") || slices.Contains(siteV2a.PrimitiveInputs, "HostObs.ApprovalStrict") {
		t.Fatalf("expected SSA trace v2a to report HostObs.PrimaryGit (not ApprovalStrict), got %v", siteV2a.PrimitiveInputs)
	}

	// Version 2b: mutate helper read behind custom_derived_gate in Go source
	// to read obs.ApprovalStrict instead of obs.PrimaryGit.
	pgV2 := writeProducerGo("return obs.ApprovalStrict")
	siteV2b := runDiscovery("{% if profile_has_recall and custom_derived_gate %}Inspect with `recall(query=\"auth\")`.{% endif %}\n", pgV2)
	if slices.Contains(siteV2b.PrimitiveInputs, "HostObs.PrimaryGit") || !slices.Contains(siteV2b.PrimitiveInputs, "HostObs.ApprovalStrict") {
		t.Fatalf("expected SSA regeneration v2b to replace HostObs.PrimaryGit with HostObs.ApprovalStrict, got %v", siteV2b.PrimitiveInputs)
	}
	if builds := interactions.DefaultGraphBuildCount(); builds > 1 {
		t.Fatalf("expected DefaultProducerGraph to build at most once per process, got %d", builds)
	}
}

// TestNewProducerPackageAndPartialLoadFailClosed verifies CT05 §2c Producer
// Coverage & Cost: moving a producer into a newly imported package outside the
// explicit root pattern list is automatically discovered across package boundaries,
// and partial/type-broken imported packages fail closed.
func TestNewProducerPackageAndPartialLoadFailClosed(t *testing.T) {
	s := mustStock(t)
	goDir := filepath.Join(t.TempDir(), "crosspkg")
	entryDir := filepath.Join(goDir, "entry")
	newPkgDir := filepath.Join(goDir, "subpkg", "newprod")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatalf("mkdir entry: %v", err)
	}
	if err := os.MkdirAll(newPkgDir, 0o755); err != nil {
		t.Fatalf("mkdir newprod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module example.com/crosspkg\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Step 1: producer lives in ./entry; load ONLY "./entry".
	entryV1 := "package entry\n\ntype Obs struct { FlagA bool; FlagB bool }\n\nfunc Build(o Obs) map[string]any {\n\treturn map[string]any{\"moved_gate\": o.FlagA}\n}\n"
	if err := os.WriteFile(filepath.Join(entryDir, "entry.go"), []byte(entryV1), 0o644); err != nil {
		t.Fatalf("write entry v1: %v", err)
	}
	pg1, err := interactions.LoadProducerGraphFromDir(goDir, "./entry")
	if err != nil {
		t.Fatalf("LoadProducerGraphFromDir v1: %v", err)
	}
	tr1 := pg1.TraceFact("moved_gate")
	if !slices.Contains(tr1.PrimitiveInputs, "Obs.FlagA") {
		t.Fatalf("expected Obs.FlagA in v1 trace, got %v", tr1.PrimitiveInputs)
	}

	// Step 2: move the producer write into newly imported ./subpkg/newprod while STILL loading only "./entry"!
	subSrc := "package newprod\n\ntype MovedInput struct { NewlyImportedFlagB bool }\n\nfunc Populate(dst map[string]any, in MovedInput) {\n\tdst[\"moved_gate\"] = in.NewlyImportedFlagB\n}\n"
	entryV2 := "package entry\n\nimport \"example.com/crosspkg/subpkg/newprod\"\n\nfunc Build(in newprod.MovedInput) map[string]any {\n\tm := map[string]any{}\n\tnewprod.Populate(m, in)\n\treturn m\n}\n"
	if err := os.WriteFile(filepath.Join(newPkgDir, "newprod.go"), []byte(subSrc), 0o644); err != nil {
		t.Fatalf("write newprod.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, "entry.go"), []byte(entryV2), 0o644); err != nil {
		t.Fatalf("write entry v2: %v", err)
	}
	pg2, err := interactions.LoadProducerGraphFromDir(goDir, "./entry")
	if err != nil {
		t.Fatalf("LoadProducerGraphFromDir v2 across imported package: %v", err)
	}
	if pg2.Stats().RootPackageCount != 1 || pg2.Stats().InScopePackageCount < 2 {
		t.Fatalf("expected RootPackageCount=1 and InScopePackageCount>=2, got %+v", pg2.Stats())
	}

	eff := buildDisposablePackCatalog(t, "acme/moved-pkg", map[string]string{
		"guidance/moved-check.md": "{% if profile_has_recall and moved_gate %}Inspect with `recall(query=\"auth\")`.{% endif %}\n",
	}, nil)
	ledger, err := interactions.BuildInteractionLedger(eff, s.Schemas, s.Names, pg2)
	if err != nil {
		t.Fatalf("BuildInteractionLedger moved pkg: %v", err)
	}
	foundMoved := slices.ContainsFunc(ledger.DiscoveredInstructions, func(st interactions.InstructionSite) bool {
		return st.DeliverySite == "guidance:moved-check" && slices.Contains(st.PrimitiveInputs, "MovedInput.NewlyImportedFlagB")
	})
	if !foundMoved {
		t.Fatalf("expected cross-package import tracing to discover MovedInput.NewlyImportedFlagB without adding ./subpkg/newprod to roots")
	}

	// Step 3: partial-load failure control — introduce a type error in the
	// imported subpackage while ./entry syntax remains valid.
	brokenSub := "package newprod\n\ntype MovedInput struct { NewlyImportedFlagB bool }\n\nfunc Populate(dst map[string]any, in MovedInput) {\n\tdst[\"moved_gate\"] = undeclaredIdentifier + 1\n}\n"
	if err := os.WriteFile(filepath.Join(newPkgDir, "newprod.go"), []byte(brokenSub), 0o644); err != nil {
		t.Fatalf("write broken newprod.go: %v", err)
	}
	if _, pErr := interactions.LoadProducerGraphFromDir(goDir, "./entry"); pErr == nil {
		t.Fatal("expected LoadProducerGraphFromDir to fail closed on partial/imported package type error")
	}
}
