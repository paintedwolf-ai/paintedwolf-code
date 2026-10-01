package ignores

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectignore"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanfixture "github.com/lycaon/lycaon/internal/scan/testfixture"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func ignoreFinding(rule, path string) api.SecurityFinding {
	finding := scanfindings.FixtureFinding(rule, api.FindingLevelHigh, rule+" message", path, 3)
	finding.Tool.DriverID = "opengrep-sast"
	return finding
}

func withKind(finding api.SecurityFinding, kind api.FindingKind) api.SecurityFinding {
	if finding.Properties == nil {
		finding.Properties = &api.SecurityFindingProperties{}
	}
	if finding.Properties.Lycaon == nil {
		finding.Properties.Lycaon = &api.SecurityFindingLycaonProperties{}
	}
	finding.Properties.Lycaon.Kind = kind
	return finding
}

// Host-owned predicates match across scanners; a rule predicate needs its scanner.
func TestIgnoreEntryConjoinsItsPredicates(t *testing.T) {
	t.Parallel()
	subject := IgnoreSubjectFor(withKind(ignoreFinding("rule-a", "test/fixtures/a.go"), api.FindingKindSAST))

	cases := []struct {
		name  string
		entry IgnoreEntry
		want  bool
	}{
		{"path glob", IgnoreEntry{Path: "test/**"}, true},
		{"bare directory covers what is under it", IgnoreEntry{Path: "test"}, true},
		{"a sibling directory does not", IgnoreEntry{Path: "src"}, false},
		{"kind", IgnoreEntry{Kind: "sast"}, true},
		{"rule glob", IgnoreEntry{Rule: "rule-*"}, true},
		{"scanner", IgnoreEntry{Scanner: "opengrep-sast"}, true},
		{"another scanner", IgnoreEntry{Scanner: "gitleaks"}, false},
		{"every predicate must hold", IgnoreEntry{Path: "test/**", Scanner: "gitleaks"}, false},
		{"one that does hold", IgnoreEntry{Path: "test/**", Kind: "sast", Scanner: "opengrep-sast"}, true},
		{"no predicate matches nothing", IgnoreEntry{}, false},
	}
	for _, tc := range cases {
		if got := tc.entry.Matches(subject); got != tc.want {
			t.Fatalf("%s: matched = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An advisory predicate matches any alias of the vulnerability.
func TestIgnoreAdvisoryMatchesEveryAlias(t *testing.T) {
	t.Parallel()
	subject := IgnoreSubjectFor(scanfixture.WithAdvisory(ignoreFinding("rule-a", "go.sum"), "GHSA-abcd", "CVE-2026-21102"))
	for _, id := range []string{"GHSA-abcd", "ghsa-abcd", "CVE-2026-21102"} {
		if !(IgnoreEntry{Advisory: id}).Matches(subject) {
			t.Fatalf("advisory %q did not reach the finding it names", id)
		}
	}
	if (IgnoreEntry{Advisory: "CVE-2026-21103"}).Matches(subject) {
		t.Fatal("a different advisory matched")
	}
}

func TestIgnoreEntryValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry IgnoreEntry
		want  error
	}{
		{"no predicate", IgnoreEntry{Reason: "noisy"}, ErrIgnoreNoPredicate},
		{"no reason", IgnoreEntry{Path: "a.go"}, ErrIgnoreNoReason},
		{"unknown kind", IgnoreEntry{Kind: "vibes", Reason: "r"}, ErrIgnoreKindInvalid},
		{"expiry is a date", IgnoreEntry{Path: "a.go", Reason: "r", Expires: "soon"}, ErrIgnoreExpiryInvalid},
		{
			"justification without an advisory",
			IgnoreEntry{Path: "a.go", Reason: "r", Justification: "vulnerable_code_not_present"},
			ErrIgnoreJustificationScope,
		},
		{
			"justification outside the VEX vocabulary",
			IgnoreEntry{Advisory: "CVE-1", Reason: "r", Justification: "we looked at it"},
			ErrIgnoreJustificationInvalid,
		},
		{"a complete entry", IgnoreEntry{Path: "a.go", Reason: "r", Expires: "2026-12-09"}, nil},
	}
	for _, tc := range cases {
		err := tc.entry.Validate()
		if tc.want == nil {
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// An expiry is exclusive: the entry stops applying on the day it names.
func TestIgnoreExpiryIsExclusive(t *testing.T) {
	t.Parallel()
	entry := IgnoreEntry{Path: "a.go", Reason: "r", Expires: "2026-09-10"}
	day := func(s string) time.Time {
		when, err := time.Parse(ignoreDateLayout, s)
		testutil.FailErr(t, "parse day", err)
		return when
	}
	if entry.Expired(day("2026-09-09")) {
		t.Fatal("an entry lapsed the day before its expiry")
	}
	if !entry.Expired(day("2026-09-10")) {
		t.Fatal("an entry outlived the day it expires on")
	}
	if (IgnoreEntry{Path: "a.go", Reason: "r"}).Expired(day("2099-01-01")) {
		t.Fatal("an entry with no expiry lapsed")
	}
}

// Ignored findings stay in the stored set.
func TestPartitionIgnoredKeepsFindingsAndNarrowsTheAgentsView(t *testing.T) {
	t.Parallel()
	catalog := &IgnoreCatalog{Rules: []IgnoreRule{{
		IgnoreEntry: IgnoreEntry{ID: "e1", Path: "test/**", Reason: "fixture material"},
		Source:      IgnoreSourceProject,
	}}}
	findings := []api.SecurityFinding{
		ignoreFinding("rule-a", "test/fixtures/a.go"),
		ignoreFinding("rule-b", "internal/api/b.go"),
	}
	active, ignored := PartitionIgnored(catalog, findings, time.Now().UTC())
	if len(active) != 1 || active[0].RuleID != "rule-b" {
		t.Fatalf("active = %+v, want only the finding no decision covers", active)
	}
	if len(ignored) != 1 || ignored[0].EntryID != "e1" || ignored[0].Reason != "fixture material" {
		t.Fatalf("ignored = %+v, want the covered finding with its decision", ignored)
	}
	if ignored[0].MatchedOn != "path: test/**" {
		t.Fatalf("matched_on = %q, want the predicate in the file's own words", ignored[0].MatchedOn)
	}
}

func TestPartitionIgnoredSkipsALapsedDecision(t *testing.T) {
	t.Parallel()
	catalog := &IgnoreCatalog{Rules: []IgnoreRule{{
		IgnoreEntry: IgnoreEntry{ID: "e1", Path: "test/**", Reason: "temporary", Expires: "2026-01-01"},
		Source:      IgnoreSourceProject,
	}}}
	active, ignored := PartitionIgnored(catalog,
		[]api.SecurityFinding{ignoreFinding("rule-a", "test/a.go")}, time.Now().UTC())
	if len(active) != 1 || len(ignored) != 0 {
		t.Fatalf("a lapsed decision still held a finding back: active=%d ignored=%d", len(active), len(ignored))
	}
}

// An invalid entry is recorded as a defect; the rest of the file still loads.
func TestIgnoreCatalogRefusesOneEntryNotTheFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeIgnoreFile(t, root, `version: 1
findings:
  - path: test/**
    reason: fixture material
  - reason: everything
  - kind: sast
    reason: static analysis is noisy here
`)
	catalog, err := LoadIgnoreCatalog([]string{root})
	testutil.FailErr(t, "load ignores", err)

	project := 0
	for _, rule := range catalog.Rules {
		if rule.Source == IgnoreSourceProject {
			project++
		}
	}
	if project != 2 {
		t.Fatalf("loaded %d project entries, want the two that validate", project)
	}
	if len(catalog.Invalid) != 1 || catalog.Invalid[0].Index != 1 {
		t.Fatalf("invalid = %+v, want the entry naming no predicate", catalog.Invalid)
	}
}

// Writes preserve comments and unmodeled content.
func TestAddIgnoreEntryPreservesHandWrittenContent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeIgnoreFile(t, root, `version: 1
findings:
  # Agreed with the platform team on 2026-08-01.
  - path: vendor/**
    reason: not our code
`)
	written, err := AddIgnoreEntry(root, IgnoreEntry{Path: "test/**", Reason: "fixture material"})
	testutil.FailErr(t, "add ignore", err)
	if written.ID == "" {
		t.Fatal("the entry was written without an id a client can withdraw")
	}

	body, err := os.ReadFile(projectignore.Path(root))
	testutil.FailErr(t, "read ignore file", err)
	for _, want := range []string{
		"# Agreed with the platform team on 2026-08-01.",
		"path: vendor/**",
		"path: test/**",
		"reason: fixture material",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("ignore file lost %q:\n%s", want, body)
		}
	}

	testutil.FailErr(t, "remove ignore", RemoveIgnoreEntry(root, written.ID))
	body, err = os.ReadFile(projectignore.Path(root))
	testutil.FailErr(t, "re-read ignore file", err)
	if strings.Contains(string(body), "test/**") {
		t.Fatalf("the withdrawn entry is still in the file:\n%s", body)
	}
	if !strings.Contains(string(body), "# Agreed with the platform team on 2026-08-01.") {
		t.Fatalf("withdrawing took a hand-written comment with it:\n%s", body)
	}
}

func TestRemoveIgnoreEntryReportsAnUnknownID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeIgnoreFile(t, root, "version: 1\nfindings: []\n")
	if err := RemoveIgnoreEntry(root, "nope"); !errors.Is(err, ErrIgnoreEntryNotFound) {
		t.Fatalf("err = %v, want ErrIgnoreEntryNotFound", err)
	}
}

func writeIgnoreFile(t *testing.T, root, body string) {
	t.Helper()
	testutil.FailErr(t, "mark ignore overlay format", settingsoverlay.EnsureCurrentFormat(root))
	dir := filepath.Join(root, settingsoverlay.DirName())
	testutil.FailErr(t, "overlay dir", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write ignore file",
		os.WriteFile(filepath.Join(dir, projectignore.FileName), []byte(body), 0o644))
}
