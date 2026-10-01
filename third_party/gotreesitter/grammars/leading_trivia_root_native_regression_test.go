package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestLeadingTriviaRootOwnershipNeedsNoResultCompatibility(t *testing.T) {
	for _, test := range leadingTriviaRootOwnershipCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			tree, err := gotreesitter.NewParser(test.language).
				ParseNoResultCompatibilityBenchmarkOnly(test.source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)
			assertLeadingTriviaRootOwnership(t, tree.RootNode(), test.language, test.source)
		})
	}
}

func TestLeadingTriviaRootOwnershipRoutes(t *testing.T) {
	gotreesitter.SetGLRForestEnabled(false)
	t.Cleanup(func() { gotreesitter.SetGLRForestEnabled(true) })

	for _, test := range leadingTriviaRootOwnershipCases() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := append(append([]byte(nil), test.source...), '\n')
			for _, receipt := range retiredDispatchRouteReceipts(t, test.language, test.source) {
				t.Run(receipt.name, func(t *testing.T) {
					assertLeadingTriviaRootOwnership(
						t,
						receipt.tree.RootNode(),
						test.language,
						source,
					)
					if receipt.name != "incremental" {
						return
					}
					profile := receipt.incrementalProfile
					if test.reuseUnsupportedReason != "" {
						if !profile.ReuseUnsupported ||
							profile.ReuseUnsupportedReason != test.reuseUnsupportedReason {
							t.Fatalf("incremental reuse status = %+v", profile)
						}
						return
					}
					if !profile.OldTreeReuseRoute || profile.ReusedSubtrees == 0 ||
						profile.ReusedBytes == 0 {
						t.Fatalf("incremental route did not reuse the old tree: %+v", profile)
					}
				})
			}
		})
	}
}

type leadingTriviaRootOwnershipCase struct {
	name     string
	source   []byte
	language *gotreesitter.Language
	// reuseUnsupportedReason records a locked grammar limitation.
	reuseUnsupportedReason string
}

func leadingTriviaRootOwnershipCases() []leadingTriviaRootOwnershipCase {
	return []leadingTriviaRootOwnershipCase{
		{
			name:     "bibtex",
			source:   []byte("\n@article{key, author={A}}"),
			language: BibtexLanguage(),
		},
		{
			name:     "cpon",
			source:   []byte("\n{\"a\":1}"),
			language: CponLanguage(),
		},
		{
			name:     "cue",
			source:   []byte("\nx: 1"),
			language: CueLanguage(),
		},
		{
			name:     "d",
			source:   []byte("\nmodule m;\nint main() { return 0; }"),
			language: DLanguage(),
		},
		{
			name:     "forth",
			source:   []byte("\n: square dup * ;"),
			language: ForthLanguage(),
		},
		{
			name:                   "kotlin",
			source:                 []byte("\nfun main() {}"),
			language:               KotlinLanguage(),
			reuseUnsupportedReason: "external_scanner_unsupported",
		},
		{
			name:     "squirrel",
			source:   []byte("\nlocal x = 1;"),
			language: SquirrelLanguage(),
		},
	}
}

func assertLeadingTriviaRootOwnership(
	t *testing.T,
	root *gotreesitter.Node,
	lang *gotreesitter.Language,
	source []byte,
) {
	t.Helper()
	if root == nil || root.HasError() {
		t.Fatalf("unexpected root: %v", root)
	}
	if got, want := root.StartByte(), uint32(1); got != want {
		t.Fatalf("%s root start = %d, want %d", lang.Name, got, want)
	}
	if got, want := root.StartPoint(), (gotreesitter.Point{Row: 1}); got != want {
		t.Fatalf("%s root point = %#v, want %#v", lang.Name, got, want)
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("%s root end = %d, want %d", lang.Name, got, want)
	}
}
