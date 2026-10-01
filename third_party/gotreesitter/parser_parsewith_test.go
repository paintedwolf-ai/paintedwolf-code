package gotreesitter

import "testing"

func TestParserLanguage(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	if got := parser.Language(); got != lang {
		t.Fatalf("Language(): got %p want %p", got, lang)
	}

	var nilParser *Parser
	if got := nilParser.Language(); got != nil {
		t.Fatalf("nil parser Language(): got %v want nil", got)
	}
}

func TestParseWithProfilingFullParseSignalsUnavailable(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	source := []byte("1+2")

	res, err := parser.ParseWith(source, WithProfiling())
	if err != nil {
		t.Fatalf("ParseWith full parse failed: %v", err)
	}
	if res.Tree == nil || res.Tree.RootNode() == nil {
		t.Fatal("ParseWith full parse returned nil tree/root")
	}
	if res.ProfileAvailable {
		t.Fatal("ProfileAvailable=true for full parse, want false")
	}
	if res.Profile != (IncrementalParseProfile{}) {
		t.Fatalf("expected zero profile for full parse, got %+v", res.Profile)
	}
}

func TestParseWithProfilingIncrementalSignalsAvailable(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	// Pin to production: the base tree must be reuse-eligible for the
	// incremental profile signals. A compact-materialized base tree is
	// hard-barred from reuse (decision 0008), zeroing those signals.
	parser.SetAdmissionCandidateRoute(false)
	oldSource := []byte("1+2")
	newSource := []byte("1+3")

	oldTree := mustParse(t, parser, oldSource)
	oldTree.Edit(InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  3,
		StartPoint:  Point{Row: 0, Column: 2},
		OldEndPoint: Point{Row: 0, Column: 3},
		NewEndPoint: Point{Row: 0, Column: 3},
	})

	res, err := parser.ParseWith(newSource, WithOldTree(oldTree), WithProfiling())
	if err != nil {
		t.Fatalf("ParseWith incremental profiled failed: %v", err)
	}
	if res.Tree == nil || res.Tree.RootNode() == nil {
		t.Fatal("ParseWith incremental returned nil tree/root")
	}
	if !res.ProfileAvailable {
		t.Fatal("ProfileAvailable=false for incremental profiled parse")
	}
	if res.Profile.MaxStacksSeen == 0 {
		t.Fatalf("expected non-zero profile data, got %+v", res.Profile)
	}
}
