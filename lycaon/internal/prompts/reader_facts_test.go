package prompts_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/testutil"
)

func expectedReaderFactKeySet(t *testing.T, eff *extpacks.EffectiveCatalog) (topKeys map[string]bool, nestedKeys map[string]map[string]bool) {
	t.Helper()
	vocab, err := prompts.BuildReaderFactVocabulary(eff)
	testutil.FailErr(t, "BuildReaderFactVocabulary", err)

	topKeys = make(map[string]bool)
	nestedKeys = make(map[string]map[string]bool)

	for _, name := range prompts.DeclaredReaderFactScalarNames() {
		if parts := strings.Split(name, "."); len(parts) == 2 {
			topKeys[parts[0]] = true
			if nestedKeys[parts[0]] == nil {
				nestedKeys[parts[0]] = make(map[string]bool)
			}
			nestedKeys[parts[0]][parts[1]] = true
			continue
		}
		topKeys[name] = true
	}

	for _, fam := range prompts.ReaderFactFamilies() {
		if fam.Nested {
			topKeys[fam.Prefix] = true
			sub := make(map[string]bool)
			switch fam.Over {
			case "tools":
				if fam.Prefix == "receipt_kind" {
					maps.Copy(sub, vocab.ReceiptTools)
				} else {
					maps.Copy(sub, vocab.Tools)
				}
			case "evidence_kinds":
				maps.Copy(sub, vocab.EvidenceKinds)
			}
			nestedKeys[fam.Prefix] = sub
			continue
		}
		switch fam.Over {
		case "tools":
			for tool := range vocab.Tools {
				topKeys[fam.Prefix+tool] = true
			}
		case "skills":
			for _, sk := range vocab.SkillKeys {
				topKeys[fam.Prefix+sk] = true
			}
		}
	}
	return topKeys, nestedKeys
}

func assertReaderFactsMatchDeclaredSet(t *testing.T, facts map[string]any, wantTop map[string]bool, wantNested map[string]map[string]bool) {
	t.Helper()
	gotTop := make(map[string]bool, len(facts))
	for k := range facts {
		gotTop[k] = true
	}
	if !reflect.DeepEqual(gotTop, wantTop) {
		var missing, extra []string
		for k := range wantTop {
			if !gotTop[k] {
				missing = append(missing, k)
			}
		}
		for k := range gotTop {
			if !wantTop[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		t.Fatalf("Reader.Facts() top-level key mismatch: missing=%v extra=%v", missing, extra)
	}
	for prefix, wantSub := range wantNested {
		gotSub := make(map[string]bool)
		switch m := facts[prefix].(type) {
		case map[string]bool:
			for k := range m {
				gotSub[k] = true
			}
		case map[string]string:
			for k := range m {
				gotSub[k] = true
			}
		case map[string]any:
			for k := range m {
				gotSub[k] = true
			}
		default:
			t.Fatalf("Reader.Facts()[%q] has unexpected type %T", prefix, facts[prefix])
		}
		if !reflect.DeepEqual(gotSub, wantSub) {
			t.Fatalf("Reader.Facts()[%q] keys = %v, want %v", prefix, gotSub, wantSub)
		}
	}
}

func TestReaderFactsKeysMatchDeclaredCatalogAcrossStateSet(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	wantTop, wantNested := expectedReaderFactKeySet(t, eff)

	surfaces, err := surfacecatalog.Load()
	testutil.FailErr(t, "load surfaces", err)
	shipped, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "load tool profiles", err)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	testutil.FailErr(t, "load agents", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	testutil.FailErr(t, "load postures", err)

	for _, posture := range postures.List() {
		profiles := slices.Clone(shipped)
		for i := range profiles {
			profiles[i].CommandEffects &= posture.CommandEffects
		}
		for _, rootCount := range []int{0, 1, 2} {
			for _, surfaceID := range surfaces.SurfaceIDs() {
				plan, err := prompts.CompileSurfacePlan(surfaceID, rootCount)
				testutil.FailErr(t, "compile plan", err)
				reader, err := prompts.NewReader(prompts.HostFacts{
					Profiles:  profiles,
					RootCount: rootCount,
					WebSearch: rootCount%2 == 1,
				}, prompts.TurnProjection{
					ProfileID: prompts.CoordinatorProfileID,
					SurfaceID: surfaceID,
					Plan:      plan,
					Offered:   (plan).ImmediateNames(),
				})
				testutil.FailErr(t, "new coordinator reader", err)
				reader = reader.WithVision(rootCount > 0)
				assertReaderFactsMatchDeclaredSet(t, reader.Facts(), wantTop, wantNested)
			}
			for _, agent := range agents {
				if agent.ID == prompts.CoordinatorProfileID {
					continue
				}
				reader, err := prompts.NewReader(prompts.HostFacts{
					Profiles:  profiles,
					RootCount: rootCount,
					HasGit:    rootCount > 0,
					WebSearch: true,
				}, prompts.TurnProjection{
					ProfileID: agent.ToolProfile,
					Declared:  true,
				})
				testutil.FailErr(t, "new worker reader", err)
				assertReaderFactsMatchDeclaredSet(t, reader.Facts(), wantTop, wantNested)
			}
		}
	}
}

func TestShippedPacksLoadWithCleanTemplateVocabulary(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)
	notes, err := prompts.CheckTemplateVocabulary(eff)
	testutil.FailErr(t, "CheckTemplateVocabulary", err)
	if len(notes) != 0 {
		t.Fatalf("shipped packs produced vocabulary notes: %v", notes)
	}
}

func TestUndeclaredReservedReaderFactFailsStockAndNotesThirdPartyAndOverlays(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)

	stockClone := *eff
	stockClone.Loaded = maps.Clone(eff.Loaded)
	stockClone.Loaded["shared/partials/synthetic-stock-typo.md"] = extpacks.UnitEffective{
		ID:           "shared/partials/synthetic-stock-typo.md",
		Kind:         "shared",
		WinnerPackID: "painted-wolf/platform",
		Content:      []byte("{% if profile_has_nonexistent_typo_tool %}bad{% endif %}"),
	}
	if _, err := prompts.CheckTemplateVocabulary(&stockClone); err == nil {
		t.Fatal("CheckTemplateVocabulary accepted undeclared reader fact in stock unit")
	}

	thirdPartyClone := *eff
	thirdPartyClone.Loaded = maps.Clone(eff.Loaded)
	thirdPartyClone.Loaded["shared/partials/synthetic-third-party-typo.md"] = extpacks.UnitEffective{
		ID:           "shared/partials/synthetic-third-party-typo.md",
		Kind:         "shared",
		WinnerPackID: "acme/custom-pack",
		Content:      []byte("{% if profile_has_nonexistent_typo_tool %}note{% endif %}"),
	}
	notes, err := prompts.CheckTemplateVocabulary(&thirdPartyClone)
	testutil.FailErr(t, "CheckTemplateVocabulary third-party", err)
	if len(notes) != 1 || !strings.Contains(notes[0], "profile_has_nonexistent_typo_tool") {
		t.Fatalf("third-party notes = %v, want 1 note naming profile_has_nonexistent_typo_tool", notes)
	}

	overlayDir := t.TempDir()
	partialsDir := filepath.Join(overlayDir, "partials")
	testutil.FailErr(t, "mkdir partials", os.MkdirAll(partialsDir, 0o755))
	testutil.FailErr(t, "write override", os.WriteFile(
		filepath.Join(partialsDir, "custom.md"),
		[]byte("{{ visual_show_nonexistent_flag }}"),
		0o600,
	))
	overlayNotes, err := prompts.CheckOverlayTemplates(eff, overlayDir)
	testutil.FailErr(t, "CheckOverlayTemplates", err)
	if len(overlayNotes) != 1 || !strings.Contains(overlayNotes[0], "visual_show_nonexistent_flag") {
		t.Fatalf("overlay notes = %v, want 1 note naming visual_show_nonexistent_flag", overlayNotes)
	}
}

func TestEveryDeclaredReaderFactScalarIsReadByAtLeastOneShippedTemplate(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)

	referenced := make(map[string]bool)
	recordRefs := func(source string) {
		for _, r := range pongoplain.VariableReferences(source) {
			referenced[r] = true
		}
	}

	for _, id := range eff.LoadedUnitIDs() {
		u := eff.Loaded[id]
		switch {
		case strings.HasPrefix(id, "agents/prompts/"),
			strings.HasPrefix(id, "shared/partials/"),
			strings.HasPrefix(id, "shared/archetypes/"):
			recordRefs(string(u.Content))
		case strings.HasPrefix(id, "shared/units/"):
			recordRefs(string(promptunit.Body(u.Content)))
		case strings.HasPrefix(id, "guidance/") && !strings.HasPrefix(id, "guidance/gate-feedback/"):
			if path, ok := eff.UnitPath(id); ok && !strings.HasSuffix(path.String(), ".md") {
				continue
			}
			recordRefs(string(u.Content))
		}
	}

	loadedSkills, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range loadedSkills {
		recordRefs(sk.Body)
		for _, res := range sk.MarkdownResources() {
			raw, err := sk.ReadResource(res)
			if err != nil {
				continue
			}
			recordRefs(string(raw))
		}
	}

	var unused []string
	for _, name := range prompts.DeclaredReaderFactScalarNames() {
		if !referenced[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		t.Fatalf("declared scalar reader facts unread by any shipped template or skill: %v", unused)
	}
}

func TestNoDeclaredReaderFactAssignedOutsideReaderAndGeneratedFile(t *testing.T) {
	root := configlayout.FindModuleRoot()
	fset := token.NewFileSet()
	var violations []string

	for _, dir := range []string{"internal", "cmd"} {
		walkRoot := filepath.Join(root, dir)
		err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "internal/prompts/reader.go" ||
				rel == "internal/prompts/reader_facts.go" ||
				rel == "internal/prompts/reader_facts.generated.go" ||
				strings.HasPrefix(rel, "internal/oar/") ||
				rel == "internal/commandsurface/reject_vars.go" {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						idx, ok := lhs.(*ast.IndexExpr)
						if !ok {
							continue
						}
						lit, ok := idx.Index.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						key, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						if prompts.IsDeclaredOrReservedReaderFact(key) {
							pos := fset.Position(lit.Pos())
							violations = append(violations, fmt.Sprintf("%s:%d assigns reader fact %q", rel, pos.Line, key))
						}
					}
				case *ast.CompositeLit:
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						lit, ok := kv.Key.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						key, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						if prompts.IsDeclaredOrReservedReaderFact(key) {
							pos := fset.Position(lit.Pos())
							violations = append(violations, fmt.Sprintf("%s:%d composite literal assigns reader fact %q", rel, pos.Line, key))
						}
					}
				}
				return true
			})
			return nil
		})
		testutil.FailErr(t, "walk "+dir, err)
	}
	if len(violations) > 0 {
		t.Fatalf("reader facts assigned outside reader.go / reader_facts.generated.go:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCheckTemplateVocabularyRejectsCapabilitySetDerivations(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "CatalogForConsumers", err)

	withUnit := func(src string) *extpacks.EffectiveCatalog {
		clone := *eff
		clone.Revision = ""
		clone.Loaded = maps.Clone(eff.Loaded)
		clone.Loaded["shared/partials/synthetic-set-check.md"] = extpacks.UnitEffective{
			ID:           "shared/partials/synthetic-set-check.md",
			Kind:         "shared",
			WinnerPackID: "painted-wolf/platform",
			Content:      []byte(src),
		}
		return &clone
	}

	rejected := []struct {
		name string
		src  string
	}{
		{
			name: "boolean and over reader facts",
			src:  `{% set local_verify = profile_has_command and profile_command_external_exec != false %}{% if local_verify %}run{% endif %}`,
		},
		{
			name: "boolean or over skill facts",
			src:  `{% set any_visual_skill = agent_has_skill_mock_before_build or agent_has_skill_verify_visual_change %}{% if any_visual_skill %}ok{% endif %}`,
		},
		{
			name: "boolean negation of reader fact",
			src:  `{% set read_only_branch = not on_worker_branch %}{% if read_only_branch %}ok{% endif %}`,
		},
		{
			name: "equality comparison on reader fact",
			src:  `{% set ask_turn = session_mode == "ask" %}{% if ask_turn %}ask{% endif %}`,
		},
		{
			name: "direct alias of boolean reader fact",
			src:  `{% set git_here = has_git %}{% if git_here %}git{% endif %}`,
		},
		{
			name: "two-hop transitive alias of boolean reader fact",
			src:  `{% set step1 = profile_has_command %}{% set step2 = step1 %}{% if step2 %}cmd{% endif %}`,
		},
		{
			name: "reserved can_ prefix on set target",
			src:  `{% set can_custom_thing = "yes" %}{{ can_custom_thing }}`,
		},
		{
			name: "reserved has_ prefix on set target",
			src:  `{% set has_custom_thing = "yes" %}{{ has_custom_thing }}`,
		},
		{
			name: "declared reader fact name on set target",
			src:  `{% set surface_card_label = "override" %}{{ surface_card_label }}`,
		},
	}
	for _, tc := range rejected {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if _, err := prompts.CheckTemplateVocabulary(withUnit(tc.src)); err == nil {
				t.Fatalf("expected CheckTemplateVocabulary to reject %q", tc.src)
			}
		})
	}

	allowed := []struct {
		name string
		src  string
	}{
		{
			name: "string display label alias",
			src:  `{% set card_label = surface_card_label %}{{ card_label }}`,
		},
		{
			name: "string cadence alias",
			src:  `{% set cadence = visual_review_cadence %}{{ cadence }}`,
		},
		{
			name: "literal string finish note",
			src:  `{% set finish_note = "complete_leg" %}{{ finish_note }}`,
		},
	}
	for _, tc := range allowed {
		t.Run("allow/"+tc.name, func(t *testing.T) {
			notes, err := prompts.CheckTemplateVocabulary(withUnit(tc.src))
			testutil.FailErr(t, "CheckTemplateVocabulary", err)
			if len(notes) > 0 {
				t.Fatalf("unexpected vocabulary notes: %v", notes)
			}
		})
	}
}
