package evidence

import "testing"

func TestLexicalRootContainedCitationPathInvariant(t *testing.T) {
	t.Parallel()

	rec := Record{
		Handle:     "read#1",
		Kind:       "read",
		Shape:      ShapeFileRegion,
		Fidelity:   FidelityStructured,
		SourceTool: "read",
		Path:       "internal/engine.go",
		LineRanges: []LineRange{{Start: 10, End: 12}},
		Body: []string{
			"10\tpackage engine",
			"11\tfunc Run() int { return 7 }",
			"12\t",
		},
	}
	ev := AssembleLedger([]Record{rec})
	roots := CitationRoots{ProjectDir: "/nonexistent/must-not-touch-fs"}

	baseline := Resolve(roots, Triple{Path: "./internal/engine.go", Line: 11}, ev, "")
	if !baseline.Verdict.Grounded() || baseline.Handle != "read#1" {
		t.Fatalf("baseline ./internal/engine.go = %+v, want grounded read#1", baseline)
	}
	baselineExcerpt := Resolve(roots, Triple{Path: "./internal/engine.go", Line: 11, Excerpt: "func Run() int { return 7 }"}, ev, "")
	if baselineExcerpt.Verdict != VerdictMatched || baselineExcerpt.Handle != "read#1" {
		t.Fatalf("baseline excerpt = %+v, want matched read#1", baselineExcerpt)
	}

	// Root-contained lexical paths must match observed ev.ByPath keys with the exact
	// same verdict as ./internal/engine.go (never raising or lowering the verdict).
	rootContained := []string{
		"./internal/../internal/engine.go",
		"pkg/../internal/engine.go",
		"a/b/../../internal/engine.go",
		"internal/sub/../engine.go",
		`pkg\..\internal\engine.go`,
	}
	for _, raw := range rootContained {
		t.Run("root_contained/"+raw, func(t *testing.T) {
			t.Parallel()
			if got := LedgerPathKey(roots, raw, ev.ByPath); got != "internal/engine.go" {
				t.Fatalf("LedgerPathKey(%q) = %q, want %q", raw, got, "internal/engine.go")
			}
			// Never accepted when byPath is nil or path is unobserved.
			if got := LedgerPathKey(roots, raw, nil); got != "" {
				t.Fatalf("LedgerPathKey(%q, nil) = %q, want empty", raw, got)
			}
			res := Resolve(roots, Triple{Path: raw, Line: 11}, ev, "")
			if res.Verdict != baseline.Verdict || res.Handle != baseline.Handle || res.Path != baseline.Path {
				t.Fatalf("Resolve(%q) = %+v, want same verdict/handle/path as baseline %+v", raw, res, baseline)
			}
			resEx := Resolve(roots, Triple{Path: raw, Line: 11, Excerpt: "func Run() int { return 7 }"}, ev, "")
			if resEx.Verdict != baselineExcerpt.Verdict || resEx.Handle != baselineExcerpt.Handle {
				t.Fatalf("Resolve excerpt(%q) = %+v, want %+v", raw, resEx, baselineExcerpt)
			}
			// Filesystem boundary functions must continue rejecting raw traversal tokens.
			if _, _, ok := ResolveCitationAbs(roots.ProjectDir, raw); ok {
				t.Fatalf("ResolveCitationAbs(%q) unexpectedly succeeded", raw)
			}
			if _, _, ok := ResolveCitationFile(roots, raw); ok {
				t.Fatalf("ResolveCitationFile(%q) unexpectedly succeeded", raw)
			}
		})
	}

	// Root-escaping, absolute, unobserved, and symlink-shaped traversal paths must stay refused.
	rejected := []string{
		"../internal/engine.go",
		"./../internal/engine.go",
		"a/../../internal/engine.go",
		"pkg/../../outside.go",
		"/internal/engine.go",
		"/etc/../internal/engine.go",
		"C:/internal/../internal/engine.go",
		"symlink-dir/../unobserved.go",
		"../symlink-escape/internal/engine.go",
		"@other/../internal/engine.go",
	}
	for _, raw := range rejected {
		t.Run("rejected/"+raw, func(t *testing.T) {
			t.Parallel()
			if got := LedgerPathKey(roots, raw, ev.ByPath); got != "" {
				t.Fatalf("LedgerPathKey(%q) = %q, want empty (rejected)", raw, got)
			}
			res := Resolve(roots, Triple{Path: raw, Line: 11}, ev, "")
			if res.Verdict != VerdictUnverifiable || res.Verdict.Grounded() {
				t.Fatalf("Resolve(%q) = %+v, want unverifiable", raw, res)
			}
		})
	}
}
