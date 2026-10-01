package grammars

import (
	"os"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// top50CorrectnessLanguages is a curated broad-coverage correctness gate used
// in addition to the lock-backed C parity suite. Keep this in user-impact
// priority order so burndowns start with high-population languages.
var top50CorrectnessLanguages = []string{
	"typescript",
	"tsx",
	"javascript",
	"python",
	"java",
	"c_sharp",
	"php",
	"bash",
	"cpp",
	"go",
	"html",
	"css",
	"sql",
	"c",
	"rust",
	"json",
	"ruby",
	"swift",
	"kotlin",
	"dart",
	"lua",
	"yaml",
	"xml",
	"toml",
	"markdown",
	"svelte",
	"scss",
	"powershell",
	"r",
	"scala",
	"hcl",
	"graphql",
	"perl",
	"elixir",
	"haskell",
	"julia",
	"clojure",
	"erlang",
	"ocaml",
	"nix",
	"objc",
	"gomod",
	"json5",
	"ini",
	"zig",
	"make",
	"cmake",
	"d",
	"awk",
	"elm",
}

func TestTop50ParseSmokeNoErrors(t *testing.T) {
	testParseSmokeNoErrors(t, top50CorrectnessLanguages, nil)
}

func TestTop50CorrectnessListMatchesLockFile(t *testing.T) {
	locked, err := loadTop50CorrectnessLockFile()
	if err != nil {
		t.Fatalf("load top50 lock file: %v", err)
	}
	if len(locked) != len(top50CorrectnessLanguages) {
		t.Fatalf("top50 list length mismatch: test has %d, lock file has %d", len(top50CorrectnessLanguages), len(locked))
	}
	for i, name := range locked {
		if top50CorrectnessLanguages[i] != name {
			t.Fatalf("top50 list mismatch at index %d: test has %q, lock file has %q", i, top50CorrectnessLanguages[i], name)
		}
	}
}

func TestCore100ParseSmokeNoErrors(t *testing.T) {
	if !includeCore100StrictSmoke() {
		t.Skip("set GTS_CORE100_STRICT_SMOKE=1 to run strict no-error smoke on Core100")
	}
	testParseSmokeNoErrors(t, Core100LanguageNames(), nil)
}

func loadTop50CorrectnessLockFile() ([]string, error) {
	source, err := os.ReadFile("update_tier1_top50.txt")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(source), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	return names, nil
}

func includeCore100StrictSmoke() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GTS_CORE100_STRICT_SMOKE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func testParseSmokeNoErrors(t *testing.T, names []string, knownErrorNodes map[string]string) {
	entries := AllLanguages()
	entryByName := make(map[string]LangEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name] = entry
	}
	t.Cleanup(func() { PurgeEmbeddedLanguageCache() })

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			entry, ok := entryByName[name]
			if !ok {
				t.Fatalf("language %q not registered", name)
			}
			lang := entry.Language()
			report := EvaluateParseSupport(entry, lang)
			src := []byte(ParseSmokeSample(name))
			parser := gotreesitter.NewParser(lang)

			var (
				tree *gotreesitter.Tree
				err  error
			)
			switch report.Backend {
			case ParseBackendTokenSource:
				if entry.TokenSourceFactory == nil {
					t.Fatalf("token source backend without factory for %q", name)
				}
				tree, err = parser.ParseWithTokenSource(src, entry.TokenSourceFactory(src, lang))
			case ParseBackendDFA, ParseBackendDFAPartial:
				tree, err = parser.Parse(src)
			default:
				t.Fatalf("unsupported parse backend %q for %q", report.Backend, name)
			}
			if err != nil {
				t.Fatalf("%s parse failed: %v", name, err)
			}
			if tree == nil || tree.RootNode() == nil {
				t.Fatalf("%s parse returned nil root", name)
			}
			defer tree.Release()

			root := tree.RootNode()
			if root.EndByte() != uint32(len(src)) {
				t.Fatalf("%s parse truncated: root.EndByte=%d sourceLen=%d", name, root.EndByte(), len(src))
			}
			if root.HasError() {
				if reason, ok := knownErrorNodes[name]; ok {
					t.Skipf("%s known degraded smoke fixture: %s", name, reason)
				}
				t.Fatalf("%s smoke sample produced error nodes", name)
			}
		})
	}
}
