package guidance_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

const liveLinkOraclePath = "testdata/live-link-oracle.json"

type liveLinkCase struct {
	Source  string `json:"source"`
	Present string `json:"present"`
}

type liveLinkCorpus struct {
	Validated []string       `json:"validated"`
	Cases     []liveLinkCase `json:"cases"`
}

// oracleLedger observes the corpus's validated URLs and one file.
func oracleLedger() evidence.Ledger {
	return evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque},
		{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, URL: "https://seen.test/a"},
		{Handle: "web#2", Kind: "web", Shape: evidence.ShapeURL, URL: "https://www.seen.test/w"},
	})
}

// The corpus pins what accepted speech presents. The frontend suite runs each present
// through the real Markdown and sanitizer path and requires every live href to be in
// the validated set, so a new way to reach an unvalidated link fails one suite or
// the other. Setting LIVE_LINK_ORACLE_WRITE rewrites the presents after a deliberate
// change; the frontend suite then judges the result.
func TestLiveLinkOracleCorpus(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(liveLinkOraclePath)
	testutil.FailErr(t, "read corpus", err)
	var corpus liveLinkCorpus
	testutil.FailErr(t, "decode corpus", json.Unmarshal(raw, &corpus))
	if len(corpus.Cases) < 1000 {
		t.Fatalf("corpus has %d cases", len(corpus.Cases))
	}
	roots := evidence.CitationRoots{}
	ev := oracleLedger()
	rewrite := os.Getenv("LIVE_LINK_ORACLE_WRITE") != ""
	for i, tc := range corpus.Cases {
		draft := guidance.ParseCloseoutDraft(tc.Source, guidance.DraftParseOptions(roots, ev))
		resolved := guidance.ResolveDraftCitations(roots, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
		got := guidance.PresentCloseoutMarkdown(draft, resolved)
		switch {
		case rewrite:
			corpus.Cases[i].Present = got
		case got != tc.Present:
			t.Errorf("%q presents %q, corpus has %q", tc.Source, got, tc.Present)
		}
	}
	if rewrite {
		out, err := json.MarshalIndent(corpus, "", " ")
		testutil.FailErr(t, "encode corpus", err)
		testutil.FailErr(t, "write corpus", os.WriteFile(liveLinkOraclePath, append(out, '\n'), 0o644))
	}
}
