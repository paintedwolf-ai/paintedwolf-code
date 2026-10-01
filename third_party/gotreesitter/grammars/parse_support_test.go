package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

var parseSmokeKnownDegraded = map[string]string{}

func parseSmokeDegradedReason(report ParseSupport, name string) string {
	if reason, ok := parseSmokeKnownDegraded[name]; ok {
		return reason
	}
	if report.Reason != "" {
		return report.Reason
	}
	return "parser reported recoverable syntax errors on smoke sample"
}

func TestSupportedLanguagesParseSmoke(t *testing.T) {
	entries := AllLanguages()
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	for _, entry := range entries {
		lang := entry.Language()
		report := EvaluateParseSupport(entry, lang)
		sample := ParseSmokeSample(report.Name)

		if report.Backend == ParseBackendUnsupported {
			t.Logf("skip %s: %s", report.Name, report.Reason)
			UnloadEmbeddedLanguage(entry.Name + ".bin")
			continue
		}

		parser := gotreesitter.NewParser(lang)
		source := []byte(sample)

		var tree *gotreesitter.Tree
		var parseErr error
		switch report.Backend {
		case ParseBackendTokenSource:
			ts := entry.TokenSourceFactory(source, lang)
			tree, parseErr = parser.ParseWithTokenSource(source, ts)
		case ParseBackendDFA, ParseBackendDFAPartial:
			tree, parseErr = parser.Parse(source)
		default:
			t.Fatalf("unknown backend %q for %q", report.Backend, report.Name)
		}
		if parseErr != nil {
			t.Fatalf("%s parse failed: %v", report.Name, parseErr)
		}

		if tree == nil || tree.RootNode() == nil {
			t.Fatalf("%s parse returned nil root using backend %q", report.Name, report.Backend)
		}
		if tree.RootNode().HasError() {
			t.Logf("%s parse smoke sample produced syntax errors (degraded): %s", report.Name, parseSmokeDegradedReason(report, report.Name))
		}
		tree.Release()

		// Release decoded grammar to keep memory bounded to ~1 grammar at a time.
		UnloadEmbeddedLanguage(entry.Name + ".bin")
	}
}
