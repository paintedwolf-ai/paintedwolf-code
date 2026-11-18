package laws_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/closure"
	"github.com/lycaon/lycaon/test/contract/laws/densource"
	"github.com/lycaon/lycaon/test/contract/laws/enumclosure"
	"github.com/lycaon/lycaon/test/contract/laws/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/laws/vendorpin"
)

// failureModes is one family's self-test: a clean tree, a seeded violation,
// a renamed anchor, a stale exemption, an empty selection and an unreadable
// or unparsable source.
type failureModes struct {
	rule                   laws.Rule
	clean, seeded, renamed map[string]string
	empty                  func(t *testing.T) *laws.Corpus
	unreadable             laws.Rule
	unreadableFiles        map[string]string
}

func (m failureModes) run(t *testing.T) {
	t.Helper()
	stale := m.rule
	stale.Exempt = append(append([]laws.Exemption(nil), m.rule.Exempt...), laws.Exemption{Key: "no-such-key", Reason: "fixture"})
	for name, tc := range map[string]struct {
		rule   laws.Rule
		corpus func(t *testing.T) *laws.Corpus
		want   outcome
	}{
		"clean":            {rule: m.rule, corpus: tree(m.clean), want: passes},
		"seeded violation": {rule: m.rule, corpus: tree(m.seeded), want: violates},
		"renamed anchor":   {rule: m.rule, corpus: tree(m.renamed), want: missesAnchor},
		"stale exemption":  {rule: stale, corpus: tree(m.clean), want: staleExemption},
		"empty corpus":     {rule: m.rule, corpus: m.empty, want: fails},
		"unreadable file":  {rule: m.unreadable, corpus: tree(m.unreadableFiles), want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, laws.Evaluate(tc.corpus(t), tc.rule), tc.want, 0)
		})
	}
}

func tree(files map[string]string) func(t *testing.T) *laws.Corpus {
	return func(t *testing.T) *laws.Corpus { return fixture(t, files) }
}

const vendorDir = "lycaon/config/runtime/scanners/rules/vendor"

func TestVendorPinFailsClosed(t *testing.T) {
	t.Parallel()
	clean := map[string]string{vendorDir + "/x/rules/rule.yaml": "rules: []\n"}
	failureModes{
		rule:  rule(t, vendorpin.Family, "scan-rule-trees-hold-only-regular-files"),
		clean: clean,
		// A nested .git turns the catalog into a submodule pointer.
		seeded:  with(clean, map[string]string{vendorDir + "/x/.git/HEAD": "ref: refs/heads/main\n"}),
		renamed: map[string]string{"lycaon/config/runtime/scanners/rules/vendored/x/rule.yaml": "rules: []\n"},
		empty: func(t *testing.T) *laws.Corpus {
			corpus := fixture(t, map[string]string{"lycaon/config/runtime/scanners/rules/README.md": "rules\n"})
			contractcheck.FailErr(t, "create the vendor directory", os.MkdirAll(filepath.Join(corpus.Root(), filepath.FromSlash(vendorDir)), 0o755))
			return corpus
		},
		// A tree git cannot read fails the tracked-files rule.
		unreadable:      rule(t, vendorpin.Family, "scan-rule-files-are-tracked"),
		unreadableFiles: clean,
	}.run(t)
}

const diagnostics = "lycaon/internal/extpacks/diagnostics.go"

func diagnosticsSource(severity string) string {
	return "package extpacks\n\nconst (\n\tDiagA = \"a\"\n\tDiagB = \"b\"\n)\n\n" +
		"var allDiagnosticCodes = []string{DiagA, DiagB}\n\n" +
		"var " + severity + " = map[string]string{DiagA: \"error\", DiagB: \"warning\"}\n"
}

func TestEnumClosureFailsClosed(t *testing.T) {
	t.Parallel()
	clean := map[string]string{diagnostics: diagnosticsSource("diagnosticSeverity")}
	extension := rule(t, enumclosure.Family, "extension-diagnostics-are-registered-and-graded")
	failureModes{
		rule:  extension,
		clean: clean,
		seeded: map[string]string{diagnostics: "package extpacks\n\nconst (\n\tDiagA = \"a\"\n\tDiagB = \"b\"\n)\n\n" +
			"var allDiagnosticCodes = []string{DiagA, DiagB}\n\nvar diagnosticSeverity = map[string]string{DiagA: \"error\"}\n"},
		renamed: map[string]string{diagnostics: diagnosticsSource("severityOf")},
		// No Diag constants: the selection is empty.
		empty: tree(map[string]string{diagnostics: "package extpacks\n\nconst CodeA = \"a\"\n\n" +
			"var allDiagnosticCodes = []string{CodeA}\n\nvar diagnosticSeverity = map[string]string{CodeA: \"error\"}\n"}),
		unreadable:      extension,
		unreadableFiles: with(clean, map[string]string{"lycaon/internal/extpacks/broken.go": "package extpacks\n\nfunc broken( {\n"}),
	}.run(t)
}

const navModel = "lycaon-den/src/settings/settings-nav-model.ts"

var navSections = `const sections = [
  { label: "General" }, { label: "Advanced" }, { label: "Approvals" }, { label: "Providers" },
  { label: "Models" }, { label: "Cost" }, { label: "Security scanners" }, { label: "Extensions" },
  { label: "Projects" }, { label: "Shortcuts" },
];
`

func TestDenSourceFailsClosed(t *testing.T) {
	t.Parallel()
	clean := map[string]string{
		navModel:                            navSections,
		"docs/guide.md":                     "Open Settings → General → Display.\n",
		"lycaon/config/packs/p/guidance.md": "See Settings → Cost.\n",
	}
	pointers := rule(t, densource.Family, "settings-pointers-name-real-sections")
	failureModes{
		rule:    pointers,
		clean:   clean,
		seeded:  with(clean, map[string]string{"docs/guide.md": "Open Settings → Nowhere to look.\n"}),
		renamed: with(clean, map[string]string{navModel: "", "lycaon-den/src/settings/nav.ts": navSections}),
		// No copy under the stock packs: the selection is empty.
		empty: tree(map[string]string{navModel: navSections, "docs/guide.md": "Open Settings → Cost.\n"}),
		// A navigation model that parses to too few labels ends the check.
		unreadable:      pointers,
		unreadableFiles: with(clean, map[string]string{navModel: `{ label: "General" }`}),
	}.run(t)
}

const debugPaths = "lycaon/internal/debugpaths/debugpaths.go"

func TestClosureFailsClosed(t *testing.T) {
	t.Parallel()
	clean := map[string]string{
		debugPaths:                 "package debugpaths\n\nfunc Files() {}\n",
		"scripts/debug-session.sh": "#!/bin/sh\necho capturing\n",
		"scripts/debug-capture.sh": "#!/bin/sh\necho done\n",
	}
	scripts := rule(t, closure.Family, "capture-scripts-name-only-catalog-files")
	failureModes{
		rule:       scripts,
		clean:      clean,
		seeded:     with(clean, map[string]string{"scripts/debug-capture.sh": "#!/bin/sh\necho > bogus-capture.jsonl\n"}),
		renamed:    with(clean, map[string]string{debugPaths: "package debugpaths\n\nfunc Catalog() {}\n"}),
		empty:      tree(with(clean, map[string]string{"scripts/debug-capture.sh": ""})),
		unreadable: scripts,
		unreadableFiles: with(clean, map[string]string{
			"lycaon/internal/debugpaths/broken.go": "package debugpaths\n\nfunc broken( {\n",
		}),
	}.run(t)
}

const promptsDir = "lycaon/internal/prompts/"

var readerFactsTree = map[string]string{
	promptsDir + "reader.go": "package prompts\n\ntype Reader struct{ facts map[string]any }\n\n" +
		"func (r Reader) Facts() map[string]any { return r.facts }\n\n" +
		"type ReaderFacts struct{ Root string }\n\nfunc (f ReaderFacts) Map() map[string]any { return map[string]any{\"root\": f.Root} }\n",
	promptsDir + "reader_facts.go":       "package prompts\n\nfunc buildReaderFacts() map[string]any { return ReaderFacts{Root: \"@a\"}.Map() }\n",
	promptsDir + "reader_facts_vocab.go": "package prompts\n\nfunc Vars(r Reader, occurrence map[string]any) (map[string]any, error) {\n\tout := r.Facts()\n\tfor k, v := range occurrence {\n\t\tout[k] = v\n\t}\n\treturn out, nil\n}\n",
}

// Reader facts reach templates only through Vars, the one site that may read
// them raw.
func TestPromptVarsThroughVarsFailsClosed(t *testing.T) {
	t.Parallel()
	failureModes{
		rule:  bare(rule(t, forbiddencalls.Family, "prompt-vars-through-vars")),
		clean: readerFactsTree,
		seeded: with(readerFactsTree, map[string]string{
			"lycaon/internal/kick/render.go": "package kick\n\nimport \"github.com/lycaon/lycaon/internal/prompts\"\n\nfunc render(r prompts.Reader) map[string]any { return r.Facts() }\n",
		}),
		renamed: with(readerFactsTree, map[string]string{
			promptsDir + "reader_facts_vocab.go": strings.ReplaceAll(readerFactsTree[promptsDir+"reader_facts_vocab.go"], "func Vars(", "func Variables("),
		}),
		empty:           tree(nil),
		unreadable:      bare(rule(t, forbiddencalls.Family, "prompt-vars-through-vars")),
		unreadableFiles: with(readerFactsTree, map[string]string{promptsDir + "broken.go": "package prompts\n\nfunc broken( {\n"}),
	}.run(t)
}
