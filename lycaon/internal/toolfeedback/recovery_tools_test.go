package toolfeedback

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

func TestRecoveryCopyUsesFrozenOfferedTools(t *testing.T) {
	bp := stockBlockPlane(t)
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true, "list_dir": true, "grep": true}}}, toolvocab.Provenance{})
	testutil.FailErr(t, "load recovery vocabulary", err)
	for _, tc := range []struct {
		name    string
		offered []string
		listing bool
	}{
		{"listing offered", []string{"read", "grep", "list_dir"}, true},
		{"narrow profile", []string{"read", "grep"}, false},
		{"no tools offered", []string{}, false},
		{"non-model caller", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.offered != nil {
				ctx = tools.WithRecoveryTools(ctx, tc.offered)
			}
			// Producer metadata cannot invent an offered recovery tool.
			err := bp.RejectObservation(ctx, "read", "plan_write_only", nil, &toolrejection.ToolReject{
				Code: "READ_IS_DIRECTORY", Data: map[string]any{"path": "plans", "can_list_dir": true},
			})
			if err == nil {
				t.Fatal("directory rejection missing")
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Copy == nil {
				t.Fatalf("missing evaluated recovery: %v", err)
			}
			calls := toolvocab.NamedTools(catalog, refusal.Copy["fix"])
			if got := slices.Contains(calls, "list_dir"); got != tc.listing {
				t.Fatalf("listing recommendation = %v, want %v: %v", got, tc.listing, calls)
			}
			for _, call := range calls {
				if !slices.Contains(tc.offered, call) {
					t.Errorf("recovery names unoffered tool %q", call)
				}
			}
		})
	}
}

func TestRecoveryFactsAreLazyAndKeepTheirRequestSnapshot(t *testing.T) {
	names := []string{"read", "list_dir"}
	ctx := tools.WithRecoveryTools(t.Context(), names)
	names[1] = "task"
	gc := oar.NewGuardContext()
	tools.RegisterRecoveryFacts(ctx, gc)
	if len(gc.ObservationData) != 0 {
		t.Fatal("recovery facts materialized before a rule referenced them")
	}
	testutil.FailErr(t, "publish selected recovery fact", gc.Ensure("paintedwolf.can_list_dir"))
	if gc.ObservationData["can_list_dir"] != true {
		t.Fatal("recovery used a later tool list")
	}
	if _, exists := gc.ObservationData["can_read"]; exists {
		t.Fatal("unreferenced recovery fact was materialized")
	}
}

func TestLocalFetchRecoveryOnlyOffersCurrentCaptureTools(t *testing.T) {
	bp := stockBlockPlane(t)
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"capture_page": true, "measure_page": true}}}, toolvocab.Provenance{})
	testutil.FailErr(t, "load browser recovery vocabulary", err)
	for _, offered := range [][]string{nil, {"capture_page"}, {"measure_page"}, {"capture_page", "measure_page"}} {
		ctx := tools.WithRecoveryTools(t.Context(), offered)
		err := bp.RejectObservation(ctx, "fetch_url", "fixture", nil, &toolrejection.ToolReject{Code: "FETCH_URL_BLOCKED", Data: map[string]any{"detail": "private address", "can_capture_page": true, "can_measure_page": true}})
		refusal, ok := guidance.RefusalFromError(err)
		if !ok || refusal.Copy == nil {
			t.Fatalf("missing refusal: %v", err)
		}
		calls := toolvocab.NamedTools(catalog, refusal.Copy["fix"])
		for _, call := range calls {
			if !slices.Contains(offered, call) {
				t.Fatalf("recovery invented %s with offered %v", call, offered)
			}
		}
		if len(offered) > 0 && len(calls) == 0 {
			t.Fatalf("offered local recovery omitted for %v", offered)
		}
	}
}

// Exercise each intrinsic rejection's declared tool-availability dimensions,
// including the short remedy that accompanies its detailed fix.
func TestRecoveryCatalogueUsesOnlyOfferedRemedies(t *testing.T) {
	bp := stockBlockPlane(t)
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalogue", err)
	boolFacts := map[string]bool{}
	for _, fact := range oar.FactCatalogue() {
		boolFacts[oar.PublishedFactName(fact.Name)] = fact.Type == "bool"
	}
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(testutil.CheckoutRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	testutil.FailErr(t, "load recovery argument schemas", err)
	checked := 0
	for _, code := range slices.Sorted(maps.Keys(cfg.HintCodes)) {
		entry := cfg.HintCodes[code]
		if entry.Anchor != oar.AnchorToolRejected || entry.Requires == nil || !slices.Contains(entry.Requires.Facts, "paintedwolf.rejection_code") {
			continue
		}
		var names, states []string
		for _, fact := range entry.Requires.Facts {
			if name, ok := strings.CutPrefix(fact, "paintedwolf.can_"); ok {
				names = append(names, name)
			} else if boolFacts[fact] {
				states = append(states, strings.TrimPrefix(fact, "paintedwolf."))
			}
		}
		if len(names) == 0 {
			continue
		}
		dimensions := len(names) + len(states)
		if dimensions > 10 || len(entry.Scenarios) == 0 {
			t.Fatalf("%s needs bounded recovery scenarios: %d dimensions, %d scenarios", code, dimensions, len(entry.Scenarios))
		}
		checked++
		for _, scenario := range entry.Scenarios {
			for mask := -1; mask < 1<<dimensions; mask++ {
				t.Run(fmt.Sprintf("%s/%s/%d", code, scenario.ID, mask), func(t *testing.T) {
					vars := guidance.ScenarioVars(entry, scenario)
					for bit, state := range states {
						if mask >= 0 {
							vars[state] = mask&(1<<(len(names)+bit)) != 0
						}
					}
					tool, _ := vars["tool"].(string)
					if tool == "" && len(entry.Tools) > 0 {
						tool = entry.Tools[0]
					}
					if tool == "" {
						tool = "read"
					}
					var offered []string
					if mask >= 0 {
						offered = []string{tool}
					}
					for bit, name := range names {
						if mask >= 0 && mask&(1<<bit) != 0 && !slices.Contains(offered, name) {
							offered = append(offered, name)
						}
					}
					ctx := t.Context()
					if mask >= 0 {
						ctx = tools.WithRecoveryTools(ctx, offered)
					}
					var first map[string]string
					for _, spoof := range []bool{false, true} {
						data := maps.Clone(vars)
						for _, name := range names {
							data["can_"+name] = spoof
						}
						err := bp.RejectObservation(ctx, tool, "fixture", nil, &toolrejection.ToolReject{Code: code, Data: data})
						refusal, ok := guidance.RefusalFromError(err)
						if !ok || refusal.Code() != code || len(refusal.Copy) == 0 {
							t.Fatalf("missing evaluated %s recovery: %v", code, err)
						}
						if first != nil && !maps.Equal(first, refusal.Copy) {
							t.Fatal("producer metadata overrode frozen recovery availability")
						}
						first = refusal.Copy
					}
					for _, field := range []string{"fix", "instead"} {
						text := first[field]
						if meta, ok := schemas.ToolMeta(tool); ok {
							for _, arg := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
								text = strings.ReplaceAll(text, "`"+arg+"` argument", "")
							}
						}
						words := strings.FieldsFunc(text, func(r rune) bool {
							return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
						})
						for _, name := range names {
							if !slices.Contains(offered, name) && slices.Contains(words, name) {
								t.Errorf("%s names unavailable %s with offered %v: %s", field, name, offered, first[field])
							}
						}
					}
				})
			}
		}
	}
	if checked == 0 {
		t.Fatal("no availability-aware intrinsic rejection was tested")
	}
	t.Logf("checked %d intrinsic rejection families", checked)
}

func TestRecoveryAvailabilityFactsMatchFrozenSchemas(t *testing.T) {
	checked := 0
	for _, fact := range oar.FactCatalogue() {
		name, ok := strings.CutPrefix(fact.Name, "paintedwolf.can_")
		if !ok {
			continue
		}
		checked++
		key := strings.TrimPrefix(fact.Name, "paintedwolf.")
		for _, offered := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", name, offered), func(t *testing.T) {
				names := []string{"different_tool"}
				if offered {
					names[0] = name
				}
				ctx := tools.WithRecoveryTools(t.Context(), names)
				names[0] = "later_tool"
				gc := oar.NewGuardContext()
				gc.ObservationData = map[string]any{key: !offered}
				tools.RegisterRecoveryFacts(ctx, gc)
				testutil.FailErr(t, "publish availability", gc.Ensure(oar.PublishedFactName(fact.Name)))
				if gc.ObservationData[key] != offered {
					t.Fatalf("%s did not follow its frozen schema membership", fact.Name)
				}
				if len(gc.ObservationData) != 1 {
					t.Fatal("unrequested recovery facts were materialized")
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("no declared recovery availability fact was tested")
	}
}
