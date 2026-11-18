package syntaxhealth

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestEverySupportedLanguageHasExplicitParseState(t *testing.T) {
	for _, language := range filekind.SupportedLanguages() {
		t.Run(language, func(t *testing.T) {
			report := Analyze(context.Background(), language, "", nil, tsparse.Validation)
			if report.Status == StatusUnsupported {
				t.Fatalf("supported language %q resolved as unsupported", language)
			}
			if report.Language != language {
				t.Fatalf("language = %q want %q", report.Language, language)
			}
		})
	}
}

func TestEvaluateNewSupportedFileMustParse(t *testing.T) {
	change := Evaluate(context.Background(), "new.py", nil, false, []byte("def broken(:\n    pass\n"))
	if change.Transition != TransitionNewFileBroken {
		t.Fatalf("transition = %q, want %q; report=%+v", change.Transition, TransitionNewFileBroken, change.After)
	}
}

func TestEvaluateExtensionlessScriptKeepsDetectedLanguage(t *testing.T) {
	before := []byte("#!/usr/bin/env python3\nprint('ready')\n")
	after := []byte("def broken(:\n    pass\n")
	change := Evaluate(context.Background(), "run", before, true, after)
	if change.After.Language != "python" || change.Transition != TransitionBrokeClean {
		t.Fatalf("transition = %+v, want Python broke_clean", change)
	}
}

func TestEvaluateNewExtensionlessScriptUsesShebang(t *testing.T) {
	after := []byte("#!/usr/bin/env python3\ndef broken(:\n    pass\n")
	change := Evaluate(context.Background(), "run", nil, false, after)
	if change.After.Language != "python" || change.Transition != TransitionNewFileBroken {
		t.Fatalf("transition = %+v, want broken new Python script", change)
	}
}

func TestEvaluateBrokenFileRequiresStrictImprovement(t *testing.T) {
	before := []byte("def first(:\n    pass\n\ndef second(:\n    pass\n")
	worse := append(append([]byte(nil), before...), []byte("\ndef third(:\n    pass\n")...)
	change := Evaluate(context.Background(), "repair.py", before, true, worse)
	if change.Transition != TransitionRepairRegressed {
		t.Fatalf("transition = %q, want %q; before=%+v after=%+v", change.Transition, TransitionRepairRegressed, change.Before, change.After)
	}
	better := []byte("def first():\n    pass\n\ndef second(:\n    pass\n")
	change = Evaluate(context.Background(), "repair.py", before, true, better)
	if change.Transition != TransitionAllowed {
		t.Fatalf("improving transition = %q, want allowed; before=%+v after=%+v", change.Transition, change.Before, change.After)
	}
}

func TestEvaluateFinalRequiresCleanSource(t *testing.T) {
	partiallyRepaired := []byte("def first():\n    pass\n\ndef second(:\n    pass\n")
	change := EvaluateFinal(context.Background(), "repair.py", partiallyRepaired)
	if change.Transition != TransitionFinalBroken {
		t.Fatalf("transition = %q, want final_broken", change.Transition)
	}
	clean := []byte("def first():\n    pass\n\ndef second():\n    pass\n")
	change = EvaluateFinal(context.Background(), "repair.py", clean)
	if change.Transition != TransitionAllowed {
		t.Fatalf("clean final transition = %q, want allowed", change.Transition)
	}
}

func TestNearestDiagnosticForDisjointRangesDoesNotUseTheirGap(t *testing.T) {
	report := Report{Diagnostics: []Diagnostic{
		{Row: 20, StartByte: 20, EndByte: 21},
		{Row: 49, StartByte: 49, EndByte: 50},
	}}
	diagnostic, seam, ok := NearestDiagnosticForRanges(report, []LineRange{{Start: 1, End: 2}, {Start: 50, End: 50}})
	if !ok || diagnostic.Row != 49 || seam.Start != 50 {
		t.Fatalf("diagnostic=%+v seam=%+v ok=%v", diagnostic, seam, ok)
	}
}
