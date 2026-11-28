package guidance_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestNonEmptyClaimInputNeverSilentlyDroppedInvariant proves GB10 I2 over every
// surface in the I1 surface table with first cases (GB06-F1, GB05-F1) and
// generated malformed/grammar-mutated inputs.
func TestNonEmptyClaimInputNeverSilentlyDroppedInvariant(t *testing.T) {
	t.Parallel()

	hintCfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)

	roots := evidence.CitationRoots{ProjectDir: "/tmp/gb10-i2"}
	ev := evidence.AssembleLedger([]evidence.Record{{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      evidence.ShapeFileRegion,
		Fidelity:   evidence.FidelityStructured,
		SourceTool: "read",
		Path:       "src/app.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 5}},
		Body: []string{
			"1\tpackage app",
			"2\tfunc Main() {}",
			"3\tfunc Helper() {}",
			"4\tconst Version = 1",
			"5\t",
		},
	}})

	malformedBySurface := map[string][]string{
		// GB06-F1 first cases + malformed ref strings.
		"record_finding_ref": {
			`src/app.go:2 "fabricated quote"`,
			`src/app.go:2 'single quoted fabricated'`,
			`src/missing.go`,
			`not-a-valid-citation-token`,
			`read#`,
			`read#0`,
			`src/app.go:abc`,
			`[read#1]`,
		},
		// GB05-F1 first cases + malformed repair directives.
		"repair_directive": {
			`[cite: c1 =>]`,
			`[cite: => read#1]`,
			`[cite: c1 -> read#1]`,
			`[cite: c1 => read#1`,
			`[cite: ]`,
			`[read#]`,
			`[cite: c1 => ]`,
		},
		"coordinator_closeout": {
			`[read#]`,
			`[cite: c1 =>]`,
			`[artifact:]`,
			`[verify:]`,
			`{"synthesis":"bare json"}`,
			`[src/app.go:5-1]`,
		},
		"mid_turn_note": {
			`[read#]`,
			`[cite: c1 => read#1]`,
			`[artifact:]`,
			`{"synthesis":"bare json"}`,
			`Observed ungrounded claim without citation.`,
		},
		"worker_report": {
			`src/app.go`,
			`src/missing.go:10`,
			`read#999`,
			`not a path`,
		},
		"submit_verdict": {
			`src/app.go`,
			`src/missing.go:10`,
			`read#999`,
			`not a path`,
		},
		"workflow_run_report": {
			"Narrative.\n\n```json\n{not valid json}\n```",
			"Narrative.\n\n```json\n{\"unknown_field\": 123}\n```",
			`[read#]`,
			`[cite: c1 =>]`,
		},
	}

	// Deterministic grammar mutations appended to every surface's corpus.
	rng := rand.New(rand.NewPCG(20261127, 0x12))
	mutatedTemplates := []string{
		"[cite: c%d =>]",
		"[cite: c%d -> read#%d]",
		"[read#%d:abc]",
		"src/gen_%d.go:%d \"mutated_%x\"",
		"[artifact:missing_%d]",
	}
	generatedMutations := make([]string, 0, 15)
	for i := range 3 {
		for _, tmpl := range mutatedTemplates {
			switch tmpl {
			case "[cite: c%d =>]", "[artifact:missing_%d]":
				generatedMutations = append(generatedMutations, fmt.Sprintf(tmpl, i+1))
			case "[cite: c%d -> read#%d]":
				generatedMutations = append(generatedMutations, fmt.Sprintf(tmpl, i+1, i+2))
			case "[read#%d:abc]":
				generatedMutations = append(generatedMutations, fmt.Sprintf(tmpl, i+1))
			default:
				generatedMutations = append(generatedMutations, fmt.Sprintf(tmpl, i, 1+rng.IntN(30), rng.Uint32()))
			}
		}
	}

	for _, surface := range citationSurfaces() {
		inputs := append([]string(nil), malformedBySurface[surface.Name]...)
		inputs = append(inputs, generatedMutations...)
		if len(inputs) == 0 {
			t.Fatalf("surface %q has no I2 malformed inputs", surface.Name)
		}
		t.Run(surface.Name, func(t *testing.T) {
			t.Parallel()
			for idx, raw := range inputs {
				outcome := surface.EvalRawInput(roots, ev, raw)
				if outcome.ParsedCitations == 0 && outcome.RefusalCode == "" {
					t.Errorf("case %d input %q was silently dropped (0 citations, no refusal code)", idx, raw)
					continue
				}
				if outcome.RefusalCode != "" {
					if _, registered := hintCfg.HintCodes[outcome.RefusalCode]; !registered {
						t.Errorf("case %d input %q returned unregistered refusal code %q", idx, raw, outcome.RefusalCode)
					}
				}
			}
		})
	}
}
