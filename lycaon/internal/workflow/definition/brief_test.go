package definition

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

// securityBriefYAML is a trimmed security scale: three questions, four levels.
const securityBriefYAML = `
question: How serious is it?
dimensions:
  - id: reachable
    label: Reachable
    question: Does untrusted input reach the flawed code?
    allow_unknown: true
    values:
      - {id: reachable, label: "Yes"}
      - {id: not_reachable, label: "No"}
  - id: outcome
    label: Worst outcome
    question: What happens if someone uses it?
    values:
      - {id: none, label: None}
      - {id: degraded, label: Service degraded}
      - {id: code_runs, label: Code runs}
  - id: attacker
    label: Attacker needs
    question: Who would have to do it?
    values:
      - {id: anyone_remote, label: Anyone remote, phrase: anyone who can reach it over the network could use it}
      - {id: already_inside, label: Already inside, phrase: only someone already in control of the app could use it}
basis: [attacker]
levels:
  - label: Critical
    means: Act now.
    tone: critical
    when:
      - {reachable: [reachable], outcome: [code_runs], attacker: [anyone_remote]}
  - label: Moderate
    means: Fix it on a normal schedule.
    tone: medium
    when:
      - {reachable: [reachable], outcome: [code_runs], attacker: [already_inside]}
  - label: Low
    means: Nothing urgent.
    tone: low
    when:
      - {reachable: [reachable], outcome: [degraded]}
  - label: None
    means: Nothing found that needs action.
    tone: good
`

func testBrief(t *testing.T) *Brief {
	t.Helper()
	var raw briefYAML
	testutil.FailErr(t, "decode brief", yaml.Unmarshal([]byte(securityBriefYAML), &raw))
	b, err := parseBriefYAML(&raw)
	testutil.FailErr(t, "parse brief", err)
	return b
}

// The first level whose condition an answer set meets decides it; answers no
// condition meets take the last level.
func TestBrief_LevelIsTheFirstConditionMet(t *testing.T) {
	b := testBrief(t)
	cases := []struct {
		answers map[string]string
		want    string
	}{
		{map[string]string{"reachable": "reachable", "outcome": "code_runs", "attacker": "anyone_remote"}, "Critical"},
		{map[string]string{"reachable": "reachable", "outcome": "code_runs", "attacker": "already_inside"}, "Moderate"},
		{map[string]string{"reachable": "reachable", "outcome": "degraded", "attacker": "anyone_remote"}, "Low"},
		{map[string]string{"reachable": "not_reachable", "outcome": "code_runs", "attacker": "anyone_remote"}, "None"},
	}
	for _, tc := range cases {
		worst, best := b.LevelRange(tc.answers)
		if worst != best || b.Levels[worst].Label != tc.want {
			t.Fatalf("answers %v rated %s..%s, want %s", tc.answers, b.Levels[worst].Label, b.Levels[best].Label, tc.want)
		}
	}
}

// An unknown answer is tried as every declared value, so the range spans each
// level it could decide.
func TestBrief_UnknownAnswerSpansTheLevelsItCouldDecide(t *testing.T) {
	b := testBrief(t)
	worst, best := b.LevelRange(map[string]string{"reachable": BriefUnknown, "outcome": "code_runs", "attacker": "anyone_remote"})
	if b.Levels[worst].Label != "Critical" || b.Levels[best].Label != "None" {
		t.Fatalf("range = %s..%s, want Critical..None", b.Levels[worst].Label, b.Levels[best].Label)
	}
}

// The run's rating is the most severe level any finding could reach and the
// most severe level one is certain to reach; the finding that set the worst
// supplies the basis phrase.
func TestBrief_RateDecidesFromTheWorstFinding(t *testing.T) {
	b := testBrief(t)
	items := []map[string]string{
		{"reachable": "not_reachable", "outcome": "none", "attacker": "anyone_remote"},
		{"reachable": "reachable", "outcome": "code_runs", "attacker": "already_inside"},
		{"reachable": "reachable", "outcome": "degraded", "attacker": "anyone_remote"},
	}
	r := b.Rate(items)
	if b.Levels[r.Worst].Label != "Moderate" || !r.Settled() || r.Decider != 1 || r.Rated != 3 {
		t.Fatalf("rating = %+v, want settled Moderate decided by finding 1", r)
	}
	if got := b.BasisPhrase(items[r.Decider]); got != "only someone already in control of the app could use it" {
		t.Fatalf("basis = %q", got)
	}
	none := b.Rate(nil)
	if b.Levels[none.Worst].Label != "None" || none.Decider != -1 {
		t.Fatalf("empty rating = %+v, want the default level and no decider", none)
	}
}

// Answers cover every declared question with a declared answer, and nothing else.
func TestBrief_CheckAnswers(t *testing.T) {
	b := testBrief(t)
	ok := map[string]string{"reachable": BriefUnknown, "outcome": "none", "attacker": "already_inside"}
	testutil.FailErr(t, "declared answers", b.CheckAnswers(ok))
	for name, bad := range map[string]map[string]string{
		"missing":    {"reachable": "reachable", "outcome": "none"},
		"undeclared": {"reachable": "reachable", "outcome": "none", "attacker": "anyone_remote", "impact": "high"},
		"off-list":   {"reachable": "reachable", "outcome": "maybe", "attacker": "anyone_remote"},
		"unknown":    {"reachable": "reachable", "outcome": BriefUnknown, "attacker": "anyone_remote"},
	} {
		if err := b.CheckAnswers(bad); err == nil {
			t.Fatalf("%s answers accepted", name)
		}
	}
	var none *Brief
	if err := none.CheckAnswers(ok); err == nil {
		t.Fatal("answers accepted without a declared rating")
	}
}

// A declaration that cannot decide every answer set is refused at load.
func TestBrief_ValidateRefusesUndecidableDeclarations(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"default with conditions": func(s string) string {
			return strings.Replace(s, "    tone: good\n", "    tone: good\n    when:\n      - {outcome: [none]}\n", 1)
		},
		"undeclared dimension in a condition": func(s string) string {
			return strings.Replace(s, "{reachable: [reachable], outcome: [degraded]}", "{impact: [high]}", 1)
		},
		"undeclared answer in a condition": func(s string) string {
			return strings.Replace(s, "outcome: [degraded]}", "outcome: [melted]}", 1)
		},
		"reserved unknown value": func(s string) string {
			return strings.Replace(s, "{id: not_reachable", "{id: unknown", 1)
		},
		"unknown tone": func(s string) string { return strings.Replace(s, "tone: low", "tone: teal", 1) },
		"basis names nothing": func(s string) string {
			return strings.Replace(s, "basis: [attacker]", "basis: [who]", 1)
		},
	} {
		var raw briefYAML
		testutil.FailErr(t, name, yaml.Unmarshal([]byte(edit(securityBriefYAML)), &raw))
		if _, err := parseBriefYAML(&raw); err == nil {
			t.Fatalf("%s: declaration accepted", name)
		}
	}
}

// The prompt lists every question with each answer it accepts, and the
// declaration round-trips through the manifest's YAML form.
func TestBrief_PromptTextAndRoundTrip(t *testing.T) {
	b := testBrief(t)
	text := b.PromptText()
	for _, want := range []string{"`reachable` — Does untrusted input reach the flawed code?", "`not_reachable` (No)", "`unknown`", "`already_inside` (Already inside)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt text = %q, want %q", text, want)
		}
	}
	back, err := parseBriefYAML(b.toYAML())
	testutil.FailErr(t, "reparse", err)
	if back.PromptText() != text || len(back.Levels) != len(b.Levels) {
		t.Fatal("brief did not survive its YAML round trip")
	}
	c := b.clone()
	c.Levels[0].When[0]["outcome"][0] = "none"
	if b.Levels[0].When[0]["outcome"][0] != "code_runs" {
		t.Fatal("clone shares conditions with its source")
	}
}

func TestBrief_LevelIndexMatchesLabelOrTone(t *testing.T) {
	b := testBrief(t)
	for want, l := range b.Levels {
		names := []string{l.Label, strings.ToLower(l.Label)}
		if l.Tone != "" {
			names = append(names, l.Tone)
		}
		for _, name := range names {
			if got, ok := b.LevelIndex(name); !ok || got != want {
				t.Fatalf("LevelIndex(%q) = %d,%v want %d", name, got, ok, want)
			}
		}
	}
	if _, ok := b.LevelIndex("severe"); ok {
		t.Fatal("an undeclared level resolved")
	}
	if got := b.LevelLabels(); len(got) != len(b.Levels) || got[0] != "Critical" || got[len(got)-1] != "None" {
		t.Fatalf("labels = %q", got)
	}
}
