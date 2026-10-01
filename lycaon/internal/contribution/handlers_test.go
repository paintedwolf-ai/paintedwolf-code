package contribution

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func compileHandlerFixture(t *testing.T) *Set {
	t.Helper()
	stockRank := func(string) ProviderRank { return RankStock }
	set, err := Compile(CompileInput{
		Units: []Input{
			{
				UnitID: "contributions/commands/focus", Kind: KindCommand,
				ProviderPackID: "painted-wolf/den",
				Body:           []byte("id: painted-wolf/den:focus\ntitle: Focus\naction: {kind: native_ui, handler: focus_composer}\n"),
			},
		},
		ProviderRank: stockRank,
	})
	testutil.FailErr(t, "compile", err)
	return set
}

func TestHandlerCoverageAcceptsExactSync(t *testing.T) {
	set := compileHandlerFixture(t)
	errs := ValidateHandlerCoverage(set, HandlerInventories{
		Den: []string{"focus_composer"},
	})
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
}

func TestHandlerCoverageRejectsDrift(t *testing.T) {
	set := compileHandlerFixture(t)

	missing := ValidateHandlerCoverage(set, HandlerInventories{})
	if len(missing) != 1 || !strings.Contains(missing[0].Error(), "focus_composer") {
		t.Fatalf("missing inventory errs = %v", missing)
	}

	undeclared := ValidateHandlerCoverage(set, HandlerInventories{
		Den: []string{"focus_composer", "orphan_handler"},
	})
	if len(undeclared) != 1 || !strings.Contains(undeclared[0].Error(), "orphan_handler") {
		t.Fatalf("undeclared errs = %v", undeclared)
	}

	classified := ValidateHandlerCoverage(set, HandlerInventories{
		Den:                 []string{"focus_composer", "orphan_handler"},
		NonCommandCallbacks: []string{"orphan_handler"},
	})
	if len(classified) != 0 {
		t.Fatalf("classified errs = %v", classified)
	}
}

func TestHandlerCoverageRejectsDuplicateDeclarations(t *testing.T) {
	stockRank := func(string) ProviderRank { return RankStock }
	set, err := Compile(CompileInput{
		Units: []Input{
			{
				UnitID: "contributions/commands/a", Kind: KindCommand,
				ProviderPackID: "painted-wolf/den",
				Body:           []byte("id: painted-wolf/den:a\ntitle: A\naction: {kind: native_ui, handler: shared_handler}\n"),
			},
			{
				UnitID: "contributions/commands/b", Kind: KindCommand,
				ProviderPackID: "painted-wolf/den",
				Body:           []byte("id: painted-wolf/den:b\ntitle: B\naction: {kind: native_ui, handler: shared_handler}\n"),
			},
		},
		ProviderRank: stockRank,
	})
	testutil.FailErr(t, "compile", err)
	errs := ValidateHandlerCoverage(set, HandlerInventories{Den: []string{"shared_handler"}})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "exactly one") {
		t.Fatalf("errs = %v", errs)
	}
}
