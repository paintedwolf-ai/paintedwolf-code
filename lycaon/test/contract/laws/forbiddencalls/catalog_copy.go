package forbiddencalls

import (
	"go/ast"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// catalogCopyWindow is long enough not to collide with common identifiers.
const catalogCopyWindow = 40

// copyPlaceholderRE matches format verbs and template tags that interrupt
// otherwise identical copy.
var copyPlaceholderRE = regexp.MustCompile(`%[+\-# 0-9.]*[a-zA-Z]|\{\{.*?\}\}|\{%.*?%\}`)

var catalogCopy = []laws.Rule{{
	// Production Go renders catalog copy instead of restating it: nothing
	// renders both, so two copies drift apart unnoticed.
	Name:    "production-go-holds-no-catalog-copy",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config/packs")},
	Exempt: []laws.Exemption{
		{Key: "internal/guidance/host_markers.go", Reason: "the single spelling of markers the host writes and reads back; the catalog and the constant are one vocabulary"},
		{Key: "internal/datamark/datamark.go", Reason: "the untrusted-retrieval teaching is rendered into every framed body as well as the prompt partial"},
	},
	Check: func(r *laws.Run) {
		windows := catalogCopyWindows(r)
		for _, f := range r.GoFiles(laws.Production, "internal") {
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if source, ok := matchCatalogCopy(value, windows); ok && !r.Exempt(f.Rel) {
					r.ReportAt(lit.Pos(), "restates catalog copy from %s; render the catalog body", source)
				}
			})
		}
	},
}}

// catalogCopyWindows indexes the first window of every prose line and policy
// copy field in the stock packs.
func catalogCopyWindows(r *laws.Run) map[string]string {
	out := map[string]string{}
	add := func(text, source string) {
		text = normalizeCopy(text)
		if len(text) < catalogCopyWindow {
			return
		}
		if _, seen := out[text[:catalogCopyWindow]]; !seen {
			out[text[:catalogCopyWindow]] = source
		}
	}
	for _, file := range r.Tree("lycaon/config/packs", ".md", ".yaml") {
		rel := "lycaon/config/packs/" + file.Rel
		switch {
		case strings.HasSuffix(rel, ".md"):
			fenced := false
			for _, line := range strings.Split(file.Text(), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
					continue
				}
				// Fenced examples and tables share format boilerplate with code.
				if !fenced && line != "" && !strings.HasPrefix(line, "|") {
					add(line, rel)
				}
			}
		case strings.Contains(rel, "/policy/"):
			var unit map[string]any
			r.Must(yaml.Unmarshal(file.Bytes(), &unit))
			for _, key := range []string{"what", "cause", "why", "fix", "instead", "message", "title"} {
				if value, ok := unit[key].(string); ok {
					add(value, rel)
				}
			}
		}
	}
	r.Expect(len(out), "catalog copy windows")
	return out
}

func matchCatalogCopy(text string, windows map[string]string) (string, bool) {
	normalized := normalizeCopy(text)
	for i := 0; i+catalogCopyWindow <= len(normalized); i++ {
		if source, ok := windows[normalized[i:i+catalogCopyWindow]]; ok {
			return source, true
		}
	}
	return "", false
}

func normalizeCopy(text string) string {
	return strings.Join(strings.Fields(copyPlaceholderRE.ReplaceAllString(text, " ")), " ")
}
