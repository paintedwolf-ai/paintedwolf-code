package repomap

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestDefinitionSpansDistinguishesEmptyAndUnsupported(t *testing.T) {
	spans, language, supported, err := DefinitionSpans(t.Context(), "go", "", []byte("package p\n"))
	testutil.FailErr(t, "analyze empty Go file", err)
	if len(spans) != 0 || language != "go" || !supported {
		t.Fatalf("empty source = %v, %q, %v", spans, language, supported)
	}
	spans, language, supported, err = DefinitionSpans(t.Context(), "unknown-grammar", "", []byte("anything"))
	testutil.FailErr(t, "analyze unsupported language", err)
	if len(spans) != 0 || language != "" || supported {
		t.Fatalf("unsupported source = %v, %q, %v", spans, language, supported)
	}
}

func TestDefinitionSpansReportsCanceledParse(t *testing.T) {
	entry := grammars.DetectLanguageByName("go")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tagger, err := gotreesitter.NewTagger(entry.Language(), grammars.ResolveTagsQuery(*entry))
	testutil.FailErr(t, "create Go tagger", err)
	_, err = tagWithRecover(tagger, func() (*gotreesitter.Tree, error) {
		return tsparse.Parse(ctx, entry.Language(), []byte("package p\nfunc Present() {}\n"), tsparse.Analysis)
	})
	var failure *tsparse.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("lost parse diagnostics: %v", err)
	}
	parsed := tagParseResult{failure: failure}
	spans, err := parsed.definitions()
	if !errors.Is(err, ErrDefinitionIncomplete) || len(spans) != 0 {
		t.Fatalf("canceled definition analysis = %v, %v; want typed incomplete", spans, err)
	}
}
