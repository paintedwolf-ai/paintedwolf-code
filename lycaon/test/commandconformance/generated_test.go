package commandconformance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestGeneratedProgramsAreDeterministicAndBounded(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load language limits", err)
	var byteLimit int64
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "program_bytes" {
			byteLimit = limit.Maximum
		}
	}
	for _, seed := range GeneratedSeeds() {
		programs, err := Generate(seed, GeneratedProgramsPerSeed)
		testutil.FailErr(t, "generate programs", err)
		again, err := Generate(seed, GeneratedProgramsPerSeed)
		testutil.FailErr(t, "repeat generation", err)
		if !reflect.DeepEqual(programs, again) {
			t.Fatal("same seed produced different grammar")
		}
		families := map[int]bool{}
		for _, p := range programs {
			if len(p.fragments) > 4 {
				t.Fatal("fragment bound exceeded")
			}
			for _, f := range p.fragments {
				families[f.kind] = true
				if f.number > 4 {
					t.Fatal("loop bound exceeded")
				}
			}
			c := p.Case()
			if int64(len(c.Program)) > byteLimit {
				t.Fatal("program exceeds catalog byte limit")
			}
			testutil.FailErr(t, "validate generated case", Validate([]Case{c}))
			parsed, err := commandsurface.ParseProgram([]byte(c.Program), commandsurface.ParseOptions{SourceName: c.ID})
			testutil.FailErr(t, "parse generated program "+c.ID+" ("+c.Program+")", err)
			testutil.FailErr(t, "close generated program", parsed.Close())
			for _, reduced := range p.ReductionCandidates() {
				if reduced.complexity() >= p.complexity() {
					t.Fatal("reduction did not decrease grammar measure")
				}
				if !reflect.DeepEqual(reduced.Case().References, c.References) {
					t.Fatal("reduction changed comparison userlands")
				}
			}
		}
		if len(families) != generatedKinds {
			t.Fatalf("seed %d covered %d grammar families", seed, len(families))
		}
	}
	for _, count := range []int{-1, 0, MaxGeneratedPrograms + 1} {
		if _, err := Generate(1, count); err == nil {
			t.Fatalf("accepted count %d", count)
		}
	}
	left, _ := Generate(7, 1)
	right, _ := Generate(8, 1)
	if left[0].Case().Program == right[0].Case().Program {
		t.Fatal("different seeds did not vary programs")
	}
}

func TestShrinkFindsAFixedPoint(t *testing.T) {
	programs, err := Generate(7, 16)
	testutil.FailErr(t, "generate shrink input", err)
	var original GeneratedProgram
	for _, p := range programs {
		if strings.Contains(p.Case().Program, "while") {
			original = p
			break
		}
	}
	retains := func(_ context.Context, cases []Case) ([]bool, error) {
		kept := make([]bool, len(cases))
		for i, c := range cases {
			kept[i] = strings.Contains(c.Program, "while")
		}
		return kept, nil
	}
	result, err := Shrink(t.Context(), original, 256, retains)
	testutil.FailErr(t, "shrink program", err)
	if !result.Minimal || len(result.Program.fragments) != 1 || !strings.Contains(result.Program.Case().Program, `-lt 1`) {
		t.Fatalf("incomplete reduction %+v", result)
	}
	for _, candidate := range result.Program.ReductionCandidates() {
		kept, err := retains(t.Context(), []Case{candidate.Case()})
		testutil.FailErr(t, "check reduction", err)
		if kept[0] {
			t.Fatal("fixed point still reduces")
		}
	}
	boundedOriginal := original
	boundedOriginal.fragments = append([]generatedFragment{{kind: 0, text: "extra", number: 4}}, original.fragments...)
	bounded, err := Shrink(t.Context(), boundedOriginal, 1, retains)
	testutil.FailErr(t, "bounded reduction", err)
	if bounded.Trials != 1 || bounded.Minimal {
		t.Fatalf("ignored trial bound %+v", bounded)
	}
	sentinel := errors.New("comparison failed")
	_, err = Shrink(t.Context(), original, 5, func(context.Context, []Case) ([]bool, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("comparison error %v", err)
	}
	_, err = Shrink(t.Context(), original, 5, func(context.Context, []Case) ([]bool, error) { return nil, nil })
	if err == nil {
		t.Fatal("accepted incomplete comparison batch")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Shrink(ctx, original, 5, retains)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation %v", err)
	}
}
