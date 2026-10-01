//go:build !grammar_subset

package grammars

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

func TestMarkdownInlineConflictPolicyRoutes(t *testing.T) {
	language := MarkdownInlineLanguage()
	tests := []struct {
		name            string
		source          []byte
		sha256          string
		wantRoutePrefix string
		rootChild       string
	}{
		{
			name:            "scorecard_smoke",
			source:          []byte("hello **world**\n"),
			sha256:          "88b7bf652dde424406390fbe63851869eb11050a5569c2773e9744cc8ca5ac90",
			wantRoutePrefix: "direct",
		},
		{
			name:            "attribute_html_tag",
			source:          []byte(`<link rel="stylesheet" href="x">`),
			sha256:          "b3499e8d88dcc40917d566772bc94bea47f92018c864e575b113ccfa6290066c",
			wantRoutePrefix: "direct",
			rootChild:       "html_tag",
		},
		{
			name:            "literal_tilde_link_destination",
			source:          []byte(`[Context](https://example.com/~user/file.pdf)`),
			sha256:          "4a2bbb87bef0ba0c446fc35f86391a4f5f1eee0da508b2249c68c8e141741398",
			wantRoutePrefix: "fallback:compact route error: parser-core fresh-full runner did not accept EOF",
		},
		{
			name:            "nested_emphasis_counterexample",
			source:          []byte(`*foo**bar**baz*`),
			sha256:          "35d595d10fb333d2b45c1364ee00ae6e8a3d7c5464184ba46a893e4a75058327",
			wantRoutePrefix: "fallback:compact route error: parser-core fresh-full runner did not accept EOF",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fmt.Sprintf("%x", sha256.Sum256(test.source)); got != test.sha256 {
				t.Fatalf("source SHA-256 = %s, want %s", got, test.sha256)
			}
			production := markdownInlineParseRoute(t, language, test.source, false)
			candidate := markdownInlineParseRoute(t, language, test.source, true)
			if !strings.HasPrefix(candidate.route, test.wantRoutePrefix) {
				t.Fatalf("compact route = %q, want prefix %q", candidate.route, test.wantRoutePrefix)
			}
			if candidate.digest != production.digest {
				t.Fatalf("compact digest = %s, production digest = %s", candidate.digest, production.digest)
			}
			if test.rootChild != "" {
				root := candidate.tree.RootNode()
				if root.HasError() || root.ChildCount() != 1 || root.Child(0).Type(language) != test.rootChild {
					t.Fatalf("compact tree = %s, want one clean %s child", root.SExpr(language), test.rootChild)
				}
			}
		})
	}

	uncertified, err := LoadLanguage("markdown_inline", BlobByName("markdown_inline"))
	if err != nil {
		t.Fatalf("load uncertified Markdown inline language: %v", err)
	}
	uncertified.ConflictPolicies = nil
	production := markdownInlineParseRoute(t, language, tests[1].source, false)
	candidate := markdownInlineParseRoute(t, uncertified, tests[1].source, true)
	if !strings.HasPrefix(candidate.route, "fallback:compact route error: parser-core fresh-full runner did not accept EOF") {
		t.Fatalf("uncertified compact route = %q, want fail-closed EOF fallback", candidate.route)
	}
	if candidate.digest != production.digest {
		t.Fatalf("uncertified digest = %s, production digest = %s", candidate.digest, production.digest)
	}
}

type markdownInlineRouteResult struct {
	route  string
	digest string
	tree   *gotreesitter.Tree
}

func markdownInlineParseRoute(t *testing.T, language *gotreesitter.Language, source []byte, compact bool) markdownInlineRouteResult {
	t.Helper()
	routedBefore, fallbackBefore := gotreesitter.AdmissionCandidateCounters()
	parser := gotreesitter.NewParser(language)
	parser.SetAdmissionCandidateRoute(compact)
	tree, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("parse compact=%t: %v", compact, err)
	}
	if tree == nil || tree.RootNode() == nil {
		t.Fatalf("parse compact=%t returned no root", compact)
	}
	t.Cleanup(tree.Release)
	inspection, err := benchfixtures.InspectGoTree(tree.RootNode(), language)
	if err != nil {
		t.Fatal(err)
	}
	if !compact {
		return markdownInlineRouteResult{route: "production", digest: inspection.SHA256, tree: tree}
	}
	routedAfter, fallbackAfter := gotreesitter.AdmissionCandidateCounters()
	switch {
	case routedAfter == routedBefore+1 && fallbackAfter == fallbackBefore:
		return markdownInlineRouteResult{route: "direct", digest: inspection.SHA256, tree: tree}
	case routedAfter == routedBefore && fallbackAfter == fallbackBefore+1:
		reason := gotreesitter.AdmissionCandidateLastFallbackReason()
		if reason == "" {
			t.Fatal("compact fallback has no reason")
		}
		return markdownInlineRouteResult{route: "fallback:" + reason, digest: inspection.SHA256, tree: tree}
	default:
		t.Fatalf("compact counters routed=%d/%d fallback=%d/%d", routedBefore, routedAfter, fallbackBefore, fallbackAfter)
		return markdownInlineRouteResult{}
	}
}
