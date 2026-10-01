package detectionpack

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const ruleUUID = "6c1f5a02-9d6f-4a1e-9b41-2f2a0c3d5e77"

func baseRule(extra string) string {
	return `title: Test rule
id: ` + ruleUUID + `
description: A test rule.
logsource:
  product: lycaon
  service: tool_exec
level: high
` + extra
}

func TestModifiers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		det  string
		cmd  string
		want bool
	}{
		{
			name: "equals",
			det: `detection:
  sel:
    Image: aws
  condition: sel`,
			cmd:  "aws s3 ls",
			want: true,
		},
		{
			name: "equals case-insensitive",
			det: `detection:
  sel:
    Image: AWS
  condition: sel`,
			cmd:  "aws s3 ls",
			want: true,
		},
		{
			name: "contains",
			det: `detection:
  sel:
    CommandLine|contains: ' rm '
  condition: sel`,
			cmd:  "aws s3 rm s3://b",
			want: true,
		},
		{
			name: "contains miss",
			det: `detection:
  sel:
    CommandLine|contains: ' rm '
  condition: sel`,
			cmd:  "aws s3 ls",
			want: false,
		},
		{
			name: "startswith",
			det: `detection:
  sel:
    Tool|startswith: comm
  condition: sel`,
			cmd:  "echo hi",
			want: true,
		},
		{
			name: "endswith",
			det: `detection:
  sel:
    Tool|endswith: and
  condition: sel`,
			cmd:  "echo hi",
			want: true,
		},
		{
			name: "contains all",
			det: `detection:
  sel:
    CommandLine|contains|all:
      - ' s3 '
      - ' rm '
  condition: sel`,
			cmd:  "aws s3 rm s3://b",
			want: true,
		},
		{
			name: "contains all miss one",
			det: `detection:
  sel:
    CommandLine|contains|all:
      - ' s3 '
      - ' rm '
  condition: sel`,
			cmd:  "aws s3 ls",
			want: false,
		},
		{
			name: "list OR default",
			det: `detection:
  sel:
    Image:
      - terraform
      - tofu
  condition: sel`,
			cmd:  "tofu destroy",
			want: true,
		},
		{
			name: "re case-sensitive",
			det: `detection:
  sel:
    CommandLine|re: 'Destroy'
  condition: sel`,
			cmd:  "terraform destroy",
			want: false,
		},
		{
			name: "re case-sensitive hit",
			det: `detection:
  sel:
    CommandLine|re: 'destroy'
  condition: sel`,
			cmd:  "terraform destroy",
			want: true,
		},
		{
			name: "re with (?i)",
			det: `detection:
  sel:
    CommandLine|re: '(?i)DESTROY'
  condition: sel`,
			cmd:  "terraform destroy",
			want: true,
		},
	}
	assertModifierCases(t, cases)
}

func assertModifierCases(t *testing.T, cases []struct {
	name string
	det  string
	cmd  string
	want bool
}) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := ParseRule([]byte(baseRule(tc.det)))
			testutil.FailErr(t, "ParseRule", err)
			if !r.Supported {
				t.Fatalf("unsupported: %s", r.UnsupportedReason)
			}
			ev := testEvent("command", tc.cmd, "/p", true, "proxy", "s")
			if got := r.Matches(ev); got != tc.want {
				t.Fatalf("Matches(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestArgvFieldMatching(t *testing.T) {
	t.Parallel()
	r, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Argv: destroy
  condition: sel`)))
	testutil.FailErr(t, "ParseRule", err)
	ev := testEvent("command", "terraform destroy -auto-approve", "/p", true, "proxy", "s")
	if !r.Matches(ev) {
		t.Fatal("expected any-token match on destroy")
	}

	r2, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Argv|contains|all:
      - terra
      - dest
  condition: sel`)))
	testutil.FailErr(t, "ParseRule", err)
	if !r2.Matches(ev) {
		t.Fatal("expected |all across tokens")
	}
	ev2 := testEvent("command", "terraform plan", "/p", true, "proxy", "s")
	if r2.Matches(ev2) {
		t.Fatal("did not expect match without dest token")
	}
}

func TestConditionGrammar(t *testing.T) {
	t.Parallel()
	mk := func(cond string) Rule {
		t.Helper()
		yaml := baseRule(`detection:
  a:
    CommandLine|contains: ' aaa '
  b:
    CommandLine|contains: ' bbb '
  c:
    CommandLine|contains: ' ccc '
  sel1:
    CommandLine|contains: ' sel1 '
  sel2:
    CommandLine|contains: ' sel2 '
  condition: ` + cond)
		r, err := ParseRule([]byte(yaml))
		testutil.FailErr(t, "ParseRule "+cond, err)
		if !r.Supported {
			t.Fatalf("%s unsupported: %s", cond, r.UnsupportedReason)
		}
		return r
	}
	match := func(r Rule, cmd string) bool {
		return r.Matches(testEvent("command", cmd, "/p", true, "proxy", "s"))
	}

	if !match(mk("a"), "x aaa y") {
		t.Fatal("a")
	}
	if !match(mk("a and b"), "aaa bbb") {
		t.Fatal("a and b")
	}
	if match(mk("a and b"), "aaa") {
		t.Fatal("a and b should miss")
	}
	if !match(mk("a or b"), "bbb") {
		t.Fatal("a or b")
	}
	if match(mk("not a"), "aaa") {
		t.Fatal("not a should miss")
	}
	if !match(mk("not a"), "zzz") {
		t.Fatal("not a")
	}
	if !match(mk("a and not b"), "aaa") {
		t.Fatal("a and not b")
	}
	if !match(mk("(a or b) and not c"), "aaa") {
		t.Fatal("(a or b) and not c")
	}
	if match(mk("(a or b) and not c"), "aaa ccc") {
		t.Fatal("(a or b) and not c should miss")
	}
	if !match(mk("1 of sel*"), "sel1") {
		t.Fatal("1 of sel*")
	}
	if !match(mk("all of sel*"), "sel1 sel2") {
		t.Fatal("all of sel*")
	}
	if match(mk("all of sel*"), "sel1") {
		t.Fatal("all of sel* should miss")
	}
	if !match(mk("1 of them"), "ccc") {
		t.Fatal("1 of them")
	}
	if !match(mk("all of them"), "aaa bbb ccc sel1 sel2") {
		t.Fatal("all of them")
	}
	if !match(mk("((a))"), "aaa") {
		t.Fatal("nested parens")
	}
}

func TestUnsupportedReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		yaml   string
		reason string
	}{
		{
			name: "unknown key",
			yaml: baseRule(`falsepositive_typo: x
detection:
  sel:
    Image: aws
  condition: sel`),
			reason: "unknown key: falsepositive_typo",
		},
		{
			name: "unsupported logsource",
			yaml: `title: T
id: ` + ruleUUID + `
description: d
logsource:
  product: windows
  service: sysmon
level: high
detection:
  sel:
    Image: aws
  condition: sel`,
			reason: "unsupported logsource",
		},
		{
			name: "unknown logsource key",
			yaml: strings.Replace(baseRule(`detection:
  sel:
    Image: aws
  condition: sel`), "  service: tool_exec", "  service: tool_exec\n  category: process", 1),
			reason: "unknown logsource key: category",
		},
		{
			name: "invalid status",
			yaml: strings.Replace(baseRule(`detection:
  sel:
    Image: aws
  condition: sel`), "level: high", "level: high\nstatus: retired", 1),
			reason: "unsupported status: retired",
		},
		{
			name: "invalid selection name",
			yaml: baseRule(`detection:
  bad-name:
    Image: aws
  condition: bad-name`),
			reason: "invalid selection name: bad-name",
		},
		{
			name: "non-string field value",
			yaml: baseRule(`detection:
  sel:
    Contained: true
  condition: sel`),
			reason: "field values must be strings",
		},
		{
			name: "unknown field",
			yaml: baseRule(`detection:
  sel:
    DestinationHostname: x
  condition: sel`),
			reason: "unknown field: DestinationHostname",
		},
		{
			name: "unsupported modifier",
			yaml: baseRule(`detection:
  sel:
    CommandLine|cidr: '10.0.0.0/8'
  condition: sel`),
			reason: "unsupported modifier: cidr",
		},
		{
			name: "re+all",
			yaml: baseRule(`detection:
  sel:
    CommandLine|re|all:
      - 'a'
      - 'b'
  condition: sel`),
			reason: "unsupported modifier combination: re+all",
		},
		{
			name: "aggregations pipe",
			yaml: baseRule(`detection:
  sel:
    Image: aws
  condition: sel | count() > 1`),
			reason: "aggregations are not supported",
		},
		{
			name: "aggregations near",
			yaml: baseRule(`detection:
  sel:
    Image: aws
  condition: sel near sel`),
			reason: "aggregations are not supported",
		},
		{
			name: "unknown selection",
			yaml: baseRule(`detection:
  sel:
    Image: aws
  condition: missing`),
			reason: "unknown selection: missing",
		},
		{
			name: "glob zero matches",
			yaml: baseRule(`detection:
  sel:
    Image: aws
  condition: 1 of xyz*`),
			reason: "selector matched no selections: xyz*",
		},
		{
			name: "unparsable condition",
			yaml: baseRule(`detection:
  sel:
    Image: aws
  condition: sel and`),
			reason: "unparsable condition:",
		},
	}
	assertUnsupportedRuleCases(t, cases)
}

func assertUnsupportedRuleCases(t *testing.T, cases []struct {
	name   string
	yaml   string
	reason string
}) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := ParseRule([]byte(tc.yaml))
			testutil.FailErr(t, "ParseRule", err)
			if r.Supported {
				t.Fatal("expected unsupported")
			}
			if r.UnsupportedReason == "" {
				t.Fatal("empty reason")
			}
			if !strings.HasPrefix(r.UnsupportedReason, tc.reason) && r.UnsupportedReason != tc.reason {
				if !strings.Contains(r.UnsupportedReason, tc.reason) {
					t.Fatalf("reason = %q, want prefix/contain %q", r.UnsupportedReason, tc.reason)
				}
			}
			ev := testEvent("command", "aws s3 rm", "/p", true, "proxy", "s")
			if r.Matches(ev) {
				t.Fatal("Matches must be false for unsupported")
			}
		})
	}
}

func TestParseRuleErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
	}{
		{"bad yaml", "{{{"},
		{"missing id", "title: T\ndescription: d\nlevel: high\nlogsource:\n  product: lycaon\n  service: tool_exec\ndetection:\n  sel:\n    Image: aws\n  condition: sel\n"},
		{"missing title", "id: " + ruleUUID + "\ndescription: d\nlevel: high\nlogsource:\n  product: lycaon\n  service: tool_exec\ndetection:\n  sel:\n    Image: aws\n  condition: sel\n"},
		{"missing description", "title: T\nid: " + ruleUUID + "\nlevel: high\nlogsource:\n  product: lycaon\n  service: tool_exec\ndetection:\n  sel:\n    Image: aws\n  condition: sel\n"},
		{"missing level", "title: T\nid: " + ruleUUID + "\ndescription: d\nlogsource:\n  product: lycaon\n  service: tool_exec\ndetection:\n  sel:\n    Image: aws\n  condition: sel\n"},
		{"missing detection", "title: T\nid: " + ruleUUID + "\ndescription: d\nlevel: high\nlogsource:\n  product: lycaon\n  service: tool_exec\n"},
		{"non-uuid id", "title: T\nid: not-a-uuid\ndescription: d\nlevel: high\nlogsource:\n  product: lycaon\n  service: tool_exec\ndetection:\n  sel:\n    Image: aws\n  condition: sel\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRule([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLogSourceFieldSetsAreClosed(t *testing.T) {
	t.Parallel()
	toolYAML := baseRule(`detection:
  sel:
    DestinationHostname: evil.example
  condition: sel`)
	r, err := ParseRule([]byte(toolYAML))
	testutil.FailErr(t, "ParseRule tool", err)
	if r.Supported || !strings.Contains(r.UnsupportedReason, "unknown field") {
		t.Fatalf("tool_exec: %+v", r)
	}

	egressYAML := `title: Egress
id: ` + ruleUUID + `
description: d
logsource:
  product: lycaon
  service: egress_observed
level: high
detection:
  sel:
    CommandLine|contains: ' aws '
  condition: sel
`
	r2, err := ParseRule([]byte(egressYAML))
	testutil.FailErr(t, "ParseRule egress", err)
	if r2.Supported || !strings.Contains(r2.UnsupportedReason, "unknown field") {
		t.Fatalf("egress_observed: %+v", r2)
	}
}

func TestMatchesRespectsSource(t *testing.T) {
	t.Parallel()
	egressYAML := `title: Stripe
id: ` + ruleUUID + `
description: d
logsource:
  product: lycaon
  service: egress_observed
level: high
detection:
  sel:
    DestinationHostname: api.stripe.com
  condition: sel
`
	r, err := ParseRule([]byte(egressYAML))
	testutil.FailErr(t, "ParseRule", err)
	if !r.Supported {
		t.Fatalf("unsupported: %s", r.UnsupportedReason)
	}
	// Fields would "satisfy" if wrongly evaluated against a tool event — still false.
	ev := testEvent("command", "curl https://api.stripe.com", "/p", true, "proxy", "s")
	if r.Matches(ev) {
		t.Fatal("Matches must be false for egress_observed rule")
	}
	eg := NewEgressEvent(EgressObservation{
		DestinationHostname: "api.stripe.com", DestinationPort: 443,
		Transport: "http_connect", Image: []string{"curl"}, SessionID: "s",
		ActionID: "a", Origin: "confined_proxy", DecisionStage: "pre_dial",
	})
	if !r.MatchesEgress(eg) {
		t.Fatal("MatchesEgress should hit")
	}

	tool, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Image: curl
  condition: sel`)))
	testutil.FailErr(t, "ParseRule tool", err)
	if tool.MatchesEgress(eg) {
		t.Fatal("MatchesEgress must be false for tool_exec rule")
	}
	if !tool.Matches(testEvent("command", "curl https://x", "/p", true, "proxy", "s")) {
		t.Fatal("Matches should hit tool rule")
	}
}
