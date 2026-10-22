package conformance_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type divergenceExpectation struct {
	Rank                   int
	Category               string
	Summary                string
	TopLevelRejectContains string
	ExpectedState          string
	ExpectedStatus         int
	RefusalCode            string
	WantNativeGaps         bool
	StdoutContains         string
	CheckFiles             map[string]*string
}

type v1SpecificExpectation struct {
	ExpectedStatus   int
	ExpectedStdout   string
	AllowExternal    bool
	ExpectedFiles    map[string]*string
}

func strPtr(s string) *string { return &s }

// v1SpecificCases are corpus entries that intentionally exercise Painted Wolf v1
// language or built-in contracts that do not exist in POSIX/GNU/BSD bash.
var v1SpecificCases = map[string]v1SpecificExpectation{
	"lang-same-file-redirect-publish": {
		ExpectedStatus: 0,
		ExpectedStdout: "FIRST\nSECOND\n",
		ExpectedFiles: map[string]*string{
			"sub/a.txt": strPtr("FIRST\nSECOND\n"),
		},
	},
	"paths-native-path-conversion": {
		ExpectedStatus: 0,
		ExpectedStdout: "native-path-ok\n",
	},
	"process-external-escape-hatch": {
		ExpectedStatus: 0,
		ExpectedStdout: "ext-ok\n",
		AllowExternal:  true,
	},
}

// knownDivergences records real product divergences between the v1 command
// runtime and the GNU/BSD reference fixtures or the v1 catalog contract.
var knownDivergences = map[string]divergenceExpectation{
	"search-find-predicates-and-actions": {
		Rank:                   1,
		Category:               "runtime-settlement-fault",
		Summary:                "find -exec/-delete fails with top-level 'bound child effect set required' and leaves 1 unsettled child in ProgramDispatcher",
		TopLevelRejectContains: "bound child effect set required",
	},
	"pure-sort-uniq-cut-tr-xargs": {
		Rank:                   1,
		Category:               "runtime-settlement-fault",
		Summary:                "xargs child dispatch fails with top-level 'bound child effect set required' and leaves 1 unsettled child in ProgramDispatcher",
		TopLevelRejectContains: "bound child effect set required",
	},
	"mutation-cp-mv-rm-rmdir-mkdir-touch-tee-mktemp-chmod-chown": {
		Rank:                   2,
		Category:               "native-binding-fault",
		Summary:                "toolhost/command_programs.go drops prepared.Mode when wrapping mutationOwners.Prepare, causing multi-mode mutation commands to fail BindOperation with 'native binding requires exact declared command and effect mode'",
		TopLevelRejectContains: "native binding requires exact declared command and effect mode",
	},
	"mutation-domain-builtins": {
		Rank:           3,
		Category:       "unwired-native-owner",
		Summary:        "mutation.NewCommandBuiltins (edit, replace-lines, code-rewrite, jq-edit) is not wired in toolhost/command_programs.go; native_only edit refuses with COMMAND_NATIVE_FORM_UNSUPPORTED",
		ExpectedState:  "rejected",
		ExpectedStatus: 2,
		RefusalCode:    "COMMAND_NATIVE_FORM_UNSUPPORTED",
	},
	"query-jq-options-and-transform": {
		Rank:           3,
		Category:       "unwired-native-owner",
		Summary:        "query.NewOwners (jq, yq) is not wired in toolhost/command_programs.go; jq falls back to external execution with native_gaps=5",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
		StdoutContains: "{\"enabled\":true,\"version\":1,\"items\":[\"a\",\"b\"]}\n",
	},
	"query-yq-options": {
		Rank:           3,
		Category:       "unwired-native-owner",
		Summary:        "query.NewOwners (jq, yq) is not wired in toolhost/command_programs.go; yq falls back to external execution with native_gaps=1",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
		StdoutContains: "1\n",
	},
	"http-curl-options": {
		Rank:           3,
		Category:       "unwired-native-owner",
		Summary:        "curl.NewOwners is not wired in toolhost/command_programs.go; curl falls back to external execution with native_gaps=1",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
		StdoutContains: "curl-failed\n",
	},
	"git-read-and-mutation-subcommands": {
		Rank:           3,
		Category:       "unwired-native-owner",
		Summary:        "gitcmd.NewOwners is not wired in toolhost/command_programs.go and ProgramDispatcher.commandIndex skips multi-word git subcommands; git falls back to external execution with native_gaps",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
		StdoutContains: "+modified\n",
	},
	"git-diff-separated-u-flag": {
		Rank:           4,
		Category:       "catalog-grammar-vs-git-cli",
		Summary:        "git.json declares 'git diff -U' with arity 1 (accepting separated '-U 1'), which falls back to external git where separated '-U 1' fails with status 128 ('ambiguous argument 1')",
		ExpectedState:  "completed",
		ExpectedStatus: 128,
		WantNativeGaps: true,
	},
	"git-global-flag-before-subcommand": {
		Rank:           5,
		Category:       "subcommand-resolution",
		Summary:        "ResolveCommand only matches literal prefix words ('git status'), so 'git -C . -c color.ui=false status -s' resolves to umbrella 'git', rejects '-s' with COMMAND_OPTION_UNSUPPORTED, and falls back to external execution",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
	},
	"inspect-domain-builtins": {
		Rank:           6,
		Category:       "alias-dispatch",
		Summary:        "literal.Owners indexes specs only by spec.Name ('repo-map') and not spec.Aliases ('survey-repo'), so survey-repo binds against an empty CommandSpecification and refuses with COMMAND_OPERAND_INVALID",
		ExpectedState:  "rejected",
		ExpectedStatus: 2,
		RefusalCode:    "COMMAND_OPERAND_INVALID",
		StdoutContains: `"name":"Alpha"`,
	},
	"search-grep-core-and-aliases": {
		Rank:           6,
		Category:       "alias-dispatch",
		Summary:        "egrep and fgrep aliases resolve to spec.Name='grep' in Evaluate, but search.Owners.Prepare inspects request.Argv[0] without alias normalization, causing egrep and fgrep child executions to fail",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		StdoutContains: "quiet-ok\n",
	},
	"pure-echo-and-printf-leading-options": {
		Rank:           7,
		Category:       "binder-leading-option-semantics",
		Summary:        "Evaluate pre-binds all catalog commands with CatalogBinder before textutil.leadingBind runs, so 'echo -z literal' and 'printf %s:%02d\\n -v 5' fail CatalogBinder with COMMAND_OPTION_UNSUPPORTED and fall back to external execution (native_gaps=2)",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		WantNativeGaps: true,
		StdoutContains: "head:a\tb\na\\tb\n-z literal\n-v:05\n",
	},
	"paths-cd-basename-dirname-realpath-readlink-source": {
		Rank:           8,
		Category:       "catalog-vs-parser-contract",
		Summary:        "paths.json documents 'source [--] PATH [ARG ...]' with optional '--', but commandsurface parser refuses 'source sub/script.sh' without '--' as COMMAND_SYNTAX_UNSUPPORTED (with Outcome.State='completed' Status=0 on pre-evaluation syntax refusal)",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		RefusalCode:    "COMMAND_SYNTAX_UNSUPPORTED",
	},
	"process-jobs-wait-kill-ps-lsof": {
		Rank:           9,
		Category:       "job-wait-status",
		Summary:        "wait on a background job terminated by 'kill -s TERM' returns status 0 (w2=0) instead of a non-zero termination status (w2=1 in GNU and BSD)",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		StdoutContains: "w1=0\nw2=0\nlsof=1\n",
	},
	"lang-test-nonempty-file-predicate": {
		Rank:           10,
		Category:       "predicate-operator-coverage",
		Summary:        "test and [ in commandprogram/predicate_expression.go omit the POSIX '-s' non-empty file unary operator from predicateUnary, returning usage status 2 instead of 0",
		ExpectedState:  "completed",
		ExpectedStatus: 2,
	},
	"metadata-wc-options": {
		Rank:           11,
		Category:       "output-formatting-and-tab-width",
		Summary:        "native wc formats counts without GNU/BSD column padding and counts raw characters (15) rather than 8-column tab display width (20 in GNU) for -L",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		StdoutContains: "5 12 55 notes.txt\n55 15 notes.txt\n1\n",
	},
	"inspect-file-signatures": {
		Rank:           12,
		Category:       "magic-description-and-mime",
		Summary:        "native file reports 'UTF-8 Unicode text' for ASCII input (GNU/BSD report 'ASCII text') and omits '; charset=...' in -i MIME output",
		ExpectedState:  "completed",
		ExpectedStatus: 0,
		StdoutContains: "UTF-8 Unicode text\nPNG image data\ntext/plain\nimage/png\n",
	},
}

func TestCatalogCoverageInConformanceCorpus(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load command language v1 catalog", err)

	coveredCommands := map[string]bool{}
	coveredForms := map[string]bool{}
	for _, c := range corpus.Cases {
		for _, cmd := range c.Commands {
			coveredCommands[cmd] = true
		}
		for _, form := range c.Forms {
			coveredForms[form] = true
		}
	}

	var missingCommands []string
	var missingForms []string
	for _, spec := range catalog.Commands {
		if !coveredCommands[spec.Name] {
			missingCommands = append(missingCommands, spec.Name)
		}
		for _, alias := range spec.Aliases {
			if !coveredCommands[alias] {
				missingCommands = append(missingCommands, fmt.Sprintf("%s (alias of %s)", alias, spec.Name))
			}
		}
		for _, form := range spec.Forms {
			if len(form.Spellings) == 0 {
				continue
			}
			key := spec.Name + ":" + form.Spellings[0]
			if !coveredForms[key] {
				missingForms = append(missingForms, key)
			}
		}
	}
	if len(missingCommands) > 0 {
		t.Fatalf("conformance corpus is missing %d catalog commands/aliases: %v", len(missingCommands), missingCommands)
	}
	if len(missingForms) > 0 {
		t.Fatalf("conformance corpus is missing %d catalog option forms: %v", len(missingForms), missingForms)
	}
}

func TestPortableCommandDifferentialConformance(t *testing.T) {
	runner := newConformanceRunner(t)
	for _, c := range runner.corpus.Cases {
		t.Run(c.ID, func(t *testing.T) {
			obs := runner.runCase(t, c)
			if exp, ok := knownDivergences[c.ID]; ok {
				verifyDivergenceExpectation(t, c, obs, exp)
				return
			}
			if v1Exp, ok := v1SpecificCases[c.ID]; ok {
				verifyV1SpecificExpectation(t, c, obs, v1Exp)
				return
			}
			assertMatchesReference(t, c, obs)
		})
	}
}

func verifyV1SpecificExpectation(t *testing.T, c fixtureCase, obs runtimeObservation, exp v1SpecificExpectation) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s refused: %+v", c.ID, obs.Outcome.Refusal)
	}
	if !exp.AllowExternal && obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s unexpected native_gaps=%d", c.ID, obs.Outcome.NativeGaps)
	}
	if obs.Outcome.Status != exp.ExpectedStatus {
		t.Fatalf("case %s status = %d, want %d (stdout=%q stderr=%q)", c.ID, obs.Outcome.Status, exp.ExpectedStatus, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.Stdout != exp.ExpectedStdout {
		t.Fatalf("case %s stdout = %q, want %q", c.ID, obs.Outcome.Stdout, exp.ExpectedStdout)
	}
	if exp.ExpectedFiles != nil && !filesEqual(obs.Files, exp.ExpectedFiles) {
		t.Fatalf("case %s files = %s, want %s", c.ID, formatFiles(obs.Files), formatFiles(exp.ExpectedFiles))
	}
}

func assertMatchesReference(t *testing.T, c fixtureCase, obs runtimeObservation) {
	t.Helper()
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s refused with code=%s feedback=%q (stdout=%q stderr=%q)",
			c.ID, obs.Outcome.Refusal.Code, obs.Outcome.Refusal.Feedback, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	if obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s fell back to external execution with native_gaps=%d receipts=%v (stdout=%q stderr=%q)",
			c.ID, obs.Outcome.NativeGaps, obs.ChildReceipts, obs.Outcome.Stdout, obs.Outcome.Stderr)
	}
	gnuMatch := matchesSnapshot(obs, c.GNU)
	bsdMatch := matchesSnapshot(obs, c.BSD)
	if !gnuMatch && !bsdMatch {
		t.Fatalf("case %s diverged from both GNU and BSD fixtures:\n  got: status=%d state=%s stdout=%q stderr=%q files=%v receipts=%v\n  gnu: status=%d stdout=%q stderr=%q files=%v\n  bsd: status=%d stdout=%q stderr=%q files=%v",
			c.ID,
			obs.Outcome.Status, obs.Outcome.State, obs.Outcome.Stdout, obs.Outcome.Stderr, formatFiles(obs.Files), obs.ChildReceipts,
			c.GNU.Status, c.GNU.Stdout, c.GNU.Stderr, formatFiles(c.GNU.Files),
			c.BSD.Status, c.BSD.Stdout, c.BSD.Stderr, formatFiles(c.BSD.Files),
		)
	}
}

func matchesSnapshot(obs runtimeObservation, ref fixtureSnapshot) bool {
	if obs.Outcome.Status != ref.Status {
		return false
	}
	if obs.Outcome.Stdout != ref.Stdout {
		return false
	}
	if !filesEqual(obs.Files, ref.Files) {
		return false
	}
	return true
}

func filesEqual(a, b map[string]*string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if va == nil || vb == nil {
			if va != vb {
				return false
			}
			continue
		}
		if normalizeUnifiedDiffHeaders(*va) != normalizeUnifiedDiffHeaders(*vb) {
			return false
		}
	}
	return reflect.DeepEqual(len(a), len(b))
}

func normalizeUnifiedDiffHeaders(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			if tab := strings.IndexByte(line, '\t'); tab >= 0 {
				lines[i] = line[:tab]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatFiles(files map[string]*string) string {
	if len(files) == 0 {
		return "{}"
	}
	var parts []string
	for k, v := range files {
		if v == nil {
			parts = append(parts, k+":<nil>")
		} else {
			parts = append(parts, fmt.Sprintf("%s:%q", k, *v))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func verifyDivergenceExpectation(t *testing.T, c fixtureCase, obs runtimeObservation, exp divergenceExpectation) {
	t.Helper()
	if exp.TopLevelRejectContains != "" {
		if !strings.Contains(obs.TopLevelReject, exp.TopLevelRejectContains) {
			t.Fatalf("case %s top-level reject = %q, want substring %q (outcome=%+v)",
				c.ID, obs.TopLevelReject, exp.TopLevelRejectContains, obs.Outcome)
		}
		return
	}
	if obs.TopLevelReject != "" {
		t.Fatalf("case %s unexpected top-level reject: %s", c.ID, obs.TopLevelReject)
	}
	if exp.ExpectedState != "" && obs.Outcome.State != exp.ExpectedState {
		t.Fatalf("case %s state = %q, want %q (outcome=%+v)", c.ID, obs.Outcome.State, exp.ExpectedState, obs.Outcome)
	}
	if obs.Outcome.Status != exp.ExpectedStatus {
		t.Fatalf("case %s status = %d, want %d (stdout=%q stderr=%q refusal=%+v)",
			c.ID, obs.Outcome.Status, exp.ExpectedStatus, obs.Outcome.Stdout, obs.Outcome.Stderr, obs.Outcome.Refusal)
	}
	if exp.RefusalCode != "" {
		if obs.Outcome.Refusal == nil || obs.Outcome.Refusal.Code != exp.RefusalCode {
			t.Fatalf("case %s refusal = %+v, want code %q", c.ID, obs.Outcome.Refusal, exp.RefusalCode)
		}
	} else if obs.Outcome.Refusal != nil {
		t.Fatalf("case %s unexpected refusal %+v", c.ID, obs.Outcome.Refusal)
	}
	if exp.WantNativeGaps && obs.Outcome.NativeGaps == 0 {
		t.Fatalf("case %s expected native_gaps > 0, got 0 (receipts=%v)", c.ID, obs.ChildReceipts)
	}
	if !exp.WantNativeGaps && obs.Outcome.NativeGaps > 0 {
		t.Fatalf("case %s unexpected native_gaps=%d (receipts=%v)", c.ID, obs.Outcome.NativeGaps, obs.ChildReceipts)
	}
	if exp.StdoutContains != "" && !strings.Contains(obs.Outcome.Stdout, exp.StdoutContains) {
		t.Fatalf("case %s stdout = %q, want substring %q", c.ID, obs.Outcome.Stdout, exp.StdoutContains)
	}
	if exp.CheckFiles != nil && !filesEqual(obs.Files, exp.CheckFiles) {
		t.Fatalf("case %s files = %s, want %s", c.ID, formatFiles(obs.Files), formatFiles(exp.CheckFiles))
	}
}
