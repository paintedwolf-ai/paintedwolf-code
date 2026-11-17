package forbiddencalls

import (
	"go/ast"
	"go/token"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// guardedMarkers are the host-written literals distinctive enough to scan
// for; the shorter ones reach Den through the same generated module.
var guardedMarkers = []string{hostmarker.VerbatimHeadTail, hostmarker.CompactionBannerOpen, hostmarker.Rejected}

// markerBoundaries precede a marker spelled as text: a quote or a regex
// anchor. An identifier character, space or brace makes it a field name.
const markerBoundaries = "\"`'^"

var frontendText = []laws.Rule{{
	// The host reads back its own markers only while both sides spell them
	// alike: Go through internal/hostmarker, Den through the generated module.
	// Comments count, since a stale example misleads the next edit.
	Name:    "host-markers-have-one-spelling",
	Anchors: []laws.Anchor{laws.Package("internal/hostmarker"), laws.File("lycaon-den/src/chat/host-markers.generated.ts")},
	Check: func(r *laws.Run) {
		report := func(rel, body string) {
			for _, marker := range guardedMarkers {
				if spellsMarker(body, marker) {
					r.Report("%s spells the host marker %q", rel, marker)
				}
			}
		}
		for _, f := range r.GoFiles(laws.Production, "internal") {
			if !laws.Under(f, "internal/hostmarker") {
				report(f.Rel, string(f.Src))
			}
		}
		for _, file := range r.Tree("lycaon-den/src", ".ts", ".tsx") {
			base := path.Base(file.Rel)
			if base == "host-markers.generated.ts" || strings.Contains(base, ".test.") || strings.Contains(base, "-fixtures.") ||
				strings.Contains(file.Rel, "testdata/") {
				continue
			}
			report("lycaon-den/src/"+file.Rel, file.Text())
		}
	},
}, {
	// Display text is cut by internal/runeclamp, on rune boundaries; a slice
	// with a marker appended can emit invalid UTF-8.
	Name:    "elision-markers-come-from-runeclamp",
	Anchors: []laws.Anchor{laws.Func("internal/runeclamp", "Clamp")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/runeclamp") {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if ok && bin.Op == token.ADD && (isElisionMarker(bin.X) || isElisionMarker(bin.Y)) && (slicesString(bin.X) || slicesString(bin.Y)) {
					r.ReportAt(bin.Pos(), "cuts text by offset and appends an elision marker; use internal/runeclamp")
				}
				return true
			})
		}
	},
}, {
	// The visual store writes under the injected artifacts directory only.
	Name:    "visual-store-has-no-home-paths",
	Anchors: []laws.Anchor{laws.Package("internal/visual")},
	Check: func(r *laws.Run) {
		needles := []string{
			"filepath.Join(home", "filepath.Join(homedir",
			`"/.paintedwolf"`, `'.paintedwolf'`, "~/.paintedwolf",
			`"/.paintedwolf-dev"`, `'.paintedwolf-dev'`, "~/.paintedwolf-dev",
			`"/.lycaon"`, `'.lycaon'`, "~/.lycaon",
			`"/.paintedwolfcode"`, `'.paintedwolfcode'`, "~/.paintedwolfcode",
		}
		for _, f := range r.GoFiles(laws.Production, "internal/visual") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "os" && name == "UserHomeDir" {
					r.ReportAt(sel.Pos(), "os.UserHomeDir; inject the artifacts directory")
				}
			})
			for _, needle := range needles {
				if strings.Contains(string(f.Src), needle) {
					r.Report("%s hardcodes the home path %q; inject the artifacts directory", f.Rel, needle)
				}
			}
		}
	},
}}

func isElisionMarker(expr ast.Expr) bool {
	value, ok := laws.StringLit(expr)
	return ok && (value == "…" || value == "...")
}

func slicesString(expr ast.Expr) bool {
	slice, ok := expr.(*ast.SliceExpr)
	return ok && slice.High != nil
}

func spellsMarker(body, marker string) bool {
	for offset := 0; ; {
		idx := strings.Index(body[offset:], marker)
		if idx < 0 {
			return false
		}
		at := offset + idx
		offset = at + 1
		if at == 0 {
			return true
		}
		if strings.ContainsRune(markerBoundaries, rune(body[at-1])) {
			return true
		}
		// A `\n` escape immediately before the marker, inside one literal.
		if at >= 2 && body[at-1] == 'n' && body[at-2] == '\\' {
			return true
		}
	}
}
