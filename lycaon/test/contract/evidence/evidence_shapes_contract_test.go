package contract

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvidenceKindShapeMapUsesOnlyLockedShapes(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	for kind, shape := range binding.KindShapeMap() {
		switch shape {
		case evidence.ShapeFileRegion, evidence.ShapeURL, evidence.ShapeCommand, evidence.ShapeArtifact,
			evidence.ShapeVisual, evidence.ShapeSurfaceSnapshot, evidence.ShapePageGeometry, evidence.ShapeStructuredEvent:
		default:
			t.Fatalf("kind %q maps to unexpected shape %q", kind, shape)
		}
	}
}

type shapeFixture struct {
	rec          evidence.Record
	validLine    int
	validExcerpt string
	validURL     string
}

func fixtureForShape(shape string, seed uint64) (shapeFixture, bool) {
	token := fmt.Sprintf("delivered_token_%x", seed)
	switch shape {
	case evidence.ShapeFileRegion:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      evidence.ShapeFileRegion,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "read",
				Path:       "internal/pkg/sample.go",
				LineRanges: []evidence.LineRange{{Start: 10, End: 12}},
				Body: []string{
					"10\tpackage sample",
					"11\tfunc Target() string { return \"" + token + "\" }",
					"12\t",
				},
			},
			validLine:    11,
			validExcerpt: "return \"" + token + "\"",
		}, true
	case evidence.ShapeURL:
		u := "https://example.test/doc/" + token
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "web_fetch#1",
				Kind:       "web_fetch",
				Shape:      evidence.ShapeURL,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "web_fetch",
				URL:        u,
				Body:       []string{"fetched " + u + " " + token},
			},
			validExcerpt: token,
			validURL:     u,
		}, true
	case evidence.ShapeCommand:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "command#1",
				Kind:       "command",
				Shape:      evidence.ShapeCommand,
				Fidelity:   evidence.FidelityOpaque,
				SourceTool: "command",
				Body:       []string{"status_code: 1", "FAIL: " + token},
			},
			validExcerpt: "FAIL: " + token,
		}, true
	case evidence.ShapeArtifact:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "artifact#1",
				Kind:       "artifact",
				Shape:      evidence.ShapeArtifact,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "create_artifact",
				Path:       "artifacts/report.md",
				Body:       []string{"artifact summary " + token},
			},
			validExcerpt: "artifact summary " + token,
		}, true
	case evidence.ShapeOpaque:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "opaque#1",
				Kind:       "opaque",
				Shape:      evidence.ShapeOpaque,
				Fidelity:   evidence.FidelityOpaque,
				SourceTool: "mcp__ext__probe",
				Body:       []string{"exit=2", "err=permission denied " + token},
			},
			validExcerpt: "err=permission denied " + token,
		}, true
	case evidence.ShapeStructuredEvent:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "event#1",
				Kind:       "event",
				Shape:      evidence.ShapeStructuredEvent,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "workflow",
				Body:       []string{"event_status=failed " + token},
			},
			validExcerpt: "event_status=failed " + token,
		}, true
	case evidence.ShapeVisual:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "visual#1",
				Kind:       "visual_mockup",
				Shape:      evidence.ShapeVisual,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "visual_mockup",
				Path:       "design/mock.png",
				Body:       []string{"mockup " + token},
			},
			validExcerpt: "mockup " + token,
		}, true
	case evidence.ShapeSurfaceSnapshot:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "snapshot#1",
				Kind:       "surface_snapshot",
				Shape:      evidence.ShapeSurfaceSnapshot,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "capture_surface",
				Path:       "captures/page.png",
				Body:       []string{"width=1280 height=720 " + token},
			},
			validExcerpt: "width=1280 height=720 " + token,
		}, true
	case evidence.ShapePageGeometry:
		return shapeFixture{
			rec: evidence.Record{
				Handle:     "geometry#1",
				Kind:       "page_geometry",
				Shape:      evidence.ShapePageGeometry,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "page_geometry",
				Body:       []string{"rect=(0,0,640,480) " + token},
			},
			validExcerpt: "rect=(0,0,640,480) " + token,
		}, true
	default:
		return shapeFixture{}, false
	}
}

func registeredEvidenceShapes(t *testing.T) []string {
	t.Helper()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	seen := map[string]bool{evidence.ShapeOpaque: true}
	for _, shape := range binding.KindShapeMap() {
		seen[shape] = true
	}
	shapes := make([]string, 0, len(seen))
	for shape := range seen {
		if _, ok := evidence.DefaultShapeRegistry().Lookup(shape); !ok {
			t.Fatalf("shape %q is not registered in DefaultShapeRegistry", shape)
		}
		shapes = append(shapes, shape)
	}
	slices.Sort(shapes)
	return shapes
}

// TestEvidenceSuppliedFieldsNeverWeakenToGroundedInvariant proves GB10 I3 across
// every shape in the evidence shape registry, including first cases V01-07,
// R03-07, R02-04, and R02-06.
func TestEvidenceSuppliedFieldsNeverWeakenToGroundedInvariant(t *testing.T) {
	t.Parallel()
	roots := evidence.CitationRoots{ProjectDir: "/tmp/gb10-i3"}

	t.Run("contradicting_supplied_fields_never_ground_across_all_shapes", func(t *testing.T) {
		t.Parallel()
		rng := rand.New(rand.NewPCG(20261127, 0x3bb1464d))
		shapes := registeredEvidenceShapes(t)
		for _, shape := range shapes {
			t.Run(shape, func(t *testing.T) {
				t.Parallel()
				seed := rng.Uint64()
				fx, ok := fixtureForShape(shape, seed)
				if !ok {
					t.Fatalf("missing I3 fixture for registered evidence shape %q", shape)
				}
				ev := evidence.AssembleLedger([]evidence.Record{fx.rec})

				// Contradicting excerpt (V01-07, R03-07): must never ground.
				badExcerpts := []string{
					"ALL TESTS PASSED",
					"status_code: 0",
					"nonexistent_excerpt",
					fmt.Sprintf("fabricated_claim_%x", seed^0x5a5a5a5a),
				}
				for _, bad := range badExcerpts {
					res := evidence.ResolveObservation(roots, ev, fx.rec.Handle, evidence.Triple{
						Path:    fx.rec.Path,
						Line:    fx.validLine,
						Excerpt: bad,
					})
					if res.Verdict.Grounded() {
						t.Errorf("shape %q handle %s contradicting excerpt %q weakened to grounded verdict %q",
							shape, fx.rec.Handle, bad, res.Verdict)
					}
					if ok, _ := evidence.VerifyRecord(fx.rec, evidence.Claim{
						Handle:  fx.rec.Handle,
						Path:    fx.rec.Path,
						Line:    fx.validLine,
						Excerpt: bad,
						URL:     "https://contradicting.example.test/never-seen",
					}); ok {
						t.Errorf("shape %q VerifyRecord accepted contradicting claim %q", shape, bad)
					}
				}

				// Contradicting path: must never ground.
				badPathRes := evidence.ResolveObservation(roots, ev, fx.rec.Handle, evidence.Triple{
					Path: "internal/contradicting/other.go",
				})
				if badPathRes.Verdict.Grounded() {
					t.Errorf("shape %q contradicting path weakened to grounded verdict %q", shape, badPathRes.Verdict)
				}

				// Unknown handle: must never ground.
				badHandleRes := evidence.ResolveObservation(roots, ev, "read#99999", evidence.Triple{
					Path:    fx.rec.Path,
					Line:    fx.validLine,
					Excerpt: fx.validExcerpt,
				})
				if badHandleRes.Verdict.Grounded() {
					t.Errorf("shape %q unknown handle weakened to grounded verdict %q", shape, badHandleRes.Verdict)
				}
			})
		}
	})

	t.Run("contradicting_delivered_line_never_grounds", func(t *testing.T) {
		t.Parallel()
		obs, err := evidence.ConfirmNumberedWindow("internal/delivered.go", "1\tpackage delivered\n2\tfunc A() {}\n3\tfunc B() {}", "resp-1", "deliv-1")
		testutil.FailErr(t, "ConfirmNumberedWindow", err)
		rec := obs.Record()
		rec.Handle = "read#1"
		ev := evidence.AssembleLedger([]evidence.Record{rec})
		for _, outOfRange := range []int{4, 15, 90, 999} {
			res := evidence.ResolveObservation(roots, ev, "read#1", evidence.Triple{
				Path: "internal/delivered.go",
				Line: outOfRange,
			})
			if res.Verdict.Grounded() {
				t.Errorf("out-of-range delivered line %d weakened to grounded verdict %q", outOfRange, res.Verdict)
			}
		}
	})

	t.Run("disagreeing_records_require_ambiguous_and_identical_bytes_stay_grounded_r02_04", func(t *testing.T) {
		t.Parallel()
		rng := rand.New(rand.NewPCG(20261127, 0x204))
		for i := range 16 {
			line := 1 + rng.IntN(40)
			path := fmt.Sprintf("internal/gen/mod_%d.go", i)
			prefix := fmt.Sprintf("func Compute_%d() int", i)
			sameBody := []string{fmt.Sprintf("%d\t%s { return 1 }", line, prefix)}
			diffBody := []string{fmt.Sprintf("%d\t%s { return 2 }", line, prefix)}

			mkRec := func(handle string, body []string) evidence.Record {
				return evidence.Record{
					Handle:     handle,
					Kind:       "read",
					Shape:      evidence.ShapeFileRegion,
					Fidelity:   evidence.FidelityStructured,
					SourceTool: "read",
					Path:       path,
					LineRanges: []evidence.LineRange{{Start: line, End: line}},
					Body:       body,
				}
			}

			// Differing bytes on two satisfying records => MUST be VerdictAmbiguous.
			evDiff := evidence.AssembleLedger([]evidence.Record{
				mkRec("read#1", sameBody),
				mkRec("read#2", diffBody),
			})
			for _, triple := range []evidence.Triple{
				{Path: path, Line: line},
				{Path: path, Line: line, Excerpt: prefix},
			} {
				res := evidence.Resolve(roots, triple, evDiff, "")
				if res.Verdict != evidence.VerdictAmbiguous || res.Verdict.Grounded() || res.Handle != "" {
					t.Errorf("case %d differing bytes triple=%+v got %+v, want ambiguous with empty handle", i, triple, res)
				}
			}

			// Identical bytes (re-read) => MUST stay grounded.
			evSame := evidence.AssembleLedger([]evidence.Record{
				mkRec("read#1", sameBody),
				mkRec("read#2", sameBody),
			})
			for _, triple := range []evidence.Triple{
				{Path: path, Line: line},
				{Path: path, Line: line, Excerpt: prefix},
			} {
				res := evidence.Resolve(roots, triple, evSame, "")
				if !res.Verdict.Grounded() || res.Verdict == evidence.VerdictAmbiguous {
					t.Errorf("case %d identical bytes re-read triple=%+v got %+v, want grounded", i, triple, res)
				}
			}
		}
	})

	t.Run("bound_requires_unique_satisfying_record_r02_06", func(t *testing.T) {
		t.Parallel()
		rng := rand.New(rand.NewPCG(20261127, 0x206))
		for i := range 12 {
			line := 5 + rng.IntN(50)
			path := fmt.Sprintf("internal/auth/check_%d.go", i)
			excerpt := fmt.Sprintf("return token_%d", i)
			rec1 := evidence.Record{
				Handle:     "read#1",
				Kind:       "read",
				Shape:      evidence.ShapeFileRegion,
				Fidelity:   evidence.FidelityStructured,
				SourceTool: "read",
				Path:       path,
				LineRanges: []evidence.LineRange{{Start: line, End: line}},
				Body:       []string{fmt.Sprintf("%d\t\t%s", line, excerpt)},
			}
			evUnique := evidence.AssembleLedger([]evidence.Record{rec1})
			triple := evidence.Triple{Path: path, Line: line, Excerpt: excerpt}

			// Stale handle hint + unique satisfying record => VerdictBound.
			bound := evidence.Resolve(roots, triple, evUnique, "read#99")
			if bound.Verdict != evidence.VerdictBound || bound.Handle != "read#1" {
				t.Errorf("case %d stale hint got %+v, want bound on read#1", i, bound)
			}
			// Exact matching hint => VerdictMatched.
			exact := evidence.Resolve(roots, triple, evUnique, "read#1")
			if exact.Verdict != evidence.VerdictMatched || exact.Handle != "read#1" {
				t.Errorf("case %d exact hint got %+v, want matched on read#1", i, exact)
			}

			// Stale handle hint + two disagreeing records => VerdictAmbiguous, never VerdictBound.
			rec2 := rec1
			rec2.Handle = "read#2"
			rec2.Body = []string{fmt.Sprintf("%d\t\t%s // mutated", line, excerpt)}
			evAmbig := evidence.AssembleLedger([]evidence.Record{rec1, rec2})
			ambig := evidence.Resolve(roots, triple, evAmbig, "read#99")
			if ambig.Verdict != evidence.VerdictAmbiguous || ambig.Verdict.Grounded() {
				t.Errorf("case %d stale hint with two disagreeing records got %+v, want ambiguous", i, ambig)
			}
		}
	})
}
