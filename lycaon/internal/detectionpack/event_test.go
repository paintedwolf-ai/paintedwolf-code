package detectionpack

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestNormalizeCommandLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"aws s3 rm", " aws s3 rm "},
		{"  aws   s3\trm\n--recursive  ", " aws s3 rm --recursive "},
		{"\n\tfoo\n", " foo "},
	}
	for _, tc := range cases {
		if got := NormalizeCommandLine(tc.in); got != tc.want {
			t.Errorf("NormalizeCommandLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitArgv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"aws s3 rm", []string{"aws", "s3", "rm"}},
		{"cat f | aws s3 cp - s3://b", []string{"cat", "f", "aws", "s3", "cp", "-", "s3://b"}},
		{"cd infra && terraform destroy", []string{"cd", "infra", "terraform", "destroy"}},
		{"echo 'hello world'; true", []string{"echo", "hello world", "true"}},
		{`echo "a b" ; ls`, []string{"echo", "a b", "ls"}},
	}
	for _, tc := range cases {
		got := SplitArgv(tc.in)
		if !stringSlicesEqual(got, tc.want) {
			t.Errorf("SplitArgv(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

// eventImages reads the stage images the production constructor resolves, so the
// resolution cases exercise the same path the gate uses.
func eventImages(raw string) []string {
	return testEvent("command", raw, "", false, "", "").Image
}

func TestEventImages(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"aws s3 rm s3://b --recursive", []string{"aws"}},
		{"/usr/local/bin/aws s3 rm s3://b", []string{"aws"}},
		{"AWS_PROFILE=prod sudo aws iam create-access-key", []string{"aws"}},
		{"aws-vault exec prod -- aws s3 rm s3://b --recursive", []string{"aws"}},
		{"doppler run -- terraform destroy", []string{"terraform"}},
		{"op run --env-file=.env -- gcloud projects delete p", []string{"gcloud"}},
		{"npx wrangler delete", []string{"wrangler"}},
		{"uv run twine upload dist/*", []string{"twine"}},
		{"poetry run aws s3 rb s3://b", []string{"aws"}},
		{"cat f | aws s3 cp - s3://b", []string{"cat", "aws"}},
		{"cd infra && terraform destroy", []string{"cd", "terraform"}},
		{"git checkout -- release-notes.md", []string{"git"}},
		{"npm publish -- --tag next", []string{"npm"}},
		{"sudo -n mkfs.ext4 /dev/sdb1", []string{"mkfs.ext4"}},
		{"nice -n 10 terraform destroy", []string{"terraform"}},
		{"env -i HOME=/tmp aws s3 ls", []string{"aws"}},
		{"time -p aws s3 ls", []string{"aws"}},
		{"stdbuf -oL aws s3 ls", []string{"aws"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := eventImages(tc.in)
		if !stringSlicesEqual(got, tc.want) {
			t.Errorf("eventImages(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestImageResolvesThroughSeparator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"aws-vault exec prod -- aws s3 rm", []string{"aws"}},
		{"doppler run -- terraform destroy", []string{"terraform"}},
		{"op run --env-file=.env -- gcloud projects delete p", []string{"gcloud"}},
		{"git checkout -- file", []string{"git"}},
		{"npm publish -- --tag x", []string{"npm"}},
		{`echo "--" && aws s3 ls`, []string{"echo", "aws"}},
	}
	for _, tc := range cases {
		got := eventImages(tc.in)
		if !stringSlicesEqual(got, tc.want) {
			t.Errorf("eventImages(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestImageResolvesThroughRunner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"npx wrangler delete", []string{"wrangler"}},
		{"uv run twine upload", []string{"twine"}},
		{"poetry run aws s3 rb s3://b", []string{"aws"}},
		{"pnpm dlx wrangler delete", []string{"wrangler"}},
		{"npm exec -- terraform destroy", []string{"terraform"}},
		{"yarn dlx aws s3 rb s3://b", []string{"aws"}},
		{"pnpm exec wrangler secret put K", []string{"wrangler"}},
		// package.json script names are not binaries.
		{"npm run terraform:destroy", []string{"npm"}},
		{"pnpm run build", []string{"pnpm"}},
		{"npx", nil},
	}
	for _, tc := range cases {
		got := eventImages(tc.in)
		if !stringSlicesEqual(got, tc.want) {
			t.Errorf("eventImages(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestStageImages(t *testing.T) {
	t.Parallel()
	if got := eventImages("cd infra && terraform destroy"); !stringSlicesEqual(got, []string{"cd", "terraform"}) {
		t.Fatalf("images = %#v", got)
	}
	ev := testEvent("command", "true && aws iam create-access-key", "/p", true, "proxy", "s1")
	if !stringSlicesEqual(ev.ApiAction, []string{"iam:create-access-key"}) {
		t.Fatalf("ApiAction = %#v", ev.ApiAction)
	}
	ev2 := testEvent("command", "terraform plan && echo destroy", "/p", true, "proxy", "s1")
	if !stringSlicesEqual(ev2.Image, []string{"terraform", "echo"}) {
		t.Fatalf("Image = %#v", ev2.Image)
	}
}

func TestImageResolutionTerminates(t *testing.T) {
	t.Parallel()
	parts := make([]string, 0, 200)
	for i := 0; i < 100; i++ {
		parts = append(parts, "sudo", "env")
	}
	parts = append(parts, "aws", "s3", "ls")
	cmd := strings.Join(parts, " ")
	got := eventImages(cmd)
	if !stringSlicesEqual(got, []string{"aws"}) {
		t.Fatalf("eventImages = %#v, want [aws]", got)
	}
}

func TestApiActionFromArgv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmd  string
		want []string
	}{
		{"aws iam create-access-key --user-name deploy", []string{"iam:create-access-key"}},
		{"aws ec2 terminate-instances --instance-ids i-1", []string{"ec2:terminate-instances"}},
		{"aws-vault exec prod -- aws sts assume-role --role-arn x", []string{"sts:assume-role"}},
		{"aws s3 rm s3://b --recursive", []string{"s3:rm"}},
		{"aws --profile prod organizations leave-organization", []string{"organizations:leave-organization"}},
		{"aws --region=us-east-1 cloudtrail delete-trail --name audit", []string{"cloudtrail:delete-trail"}},
		{"aws --no-cli-pager --output json s3 ls", []string{"s3:ls"}},
		{"cd infra && aws sts assume-role --role-arn x", []string{"sts:assume-role"}},
		{"aws --version", nil},
		{"aws s3", nil},
		{"az group delete -n prod", nil},
		{"gcloud projects delete p", nil},
		{"kubectl delete ns prod", nil},
	}
	for _, tc := range cases {
		ev := testEvent("command", tc.cmd, "/p", true, "proxy", "s")
		if !stringSlicesEqual(ev.ApiAction, tc.want) {
			t.Errorf("ApiAction(%q) = %#v, want %#v", tc.cmd, ev.ApiAction, tc.want)
		}
	}
}

func TestRulesDoNotCombineIndependentShellStages(t *testing.T) {
	t.Parallel()
	r, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Image: aws
    CommandLine|contains|all:
      - ' s3 '
      - ' rb '
  condition: sel`)))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	if r.Matches(testEvent("command", "aws s3 ls && echo rb", "/p", true, "proxy", "s")) {
		t.Fatal("tokens from an independent shell stage must not satisfy an aws rule")
	}
	if !r.Matches(testEvent("command", "cd infra && aws s3 rb s3://bucket", "/p", true, "proxy", "s")) {
		t.Fatal("a complete match in one shell stage must still fire")
	}

	// Pipeline rules match PipelineCommandLine; CommandLine holds one process.
	pipe, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Image: curl
    PipelineCommandLine|contains|all:
      - ' | '
      - ' sh '
  condition: sel`)))
	if err != nil {
		t.Fatalf("ParseRule pipeline: %v", err)
	}
	if !pipe.Matches(testEvent("command", "curl https://example.test/install | sh", "/p", true, "proxy", "s")) {
		t.Fatal("a connected pipeline must stay matchable through PipelineCommandLine")
	}

	straddle, err := ParseRule([]byte(baseRule(`detection:
  sel:
    Image: gcloud
    CommandLine|contains: ' allusers'
  condition: sel`)))
	if err != nil {
		t.Fatalf("ParseRule straddle: %v", err)
	}
	if straddle.Matches(testEvent("command",
		"gcloud storage buckets get-iam-policy gs://x | grep allUsers", "/p", true, "proxy", "s")) {
		t.Fatal("CommandLine must not carry tokens from the process on the other side of a pipe")
	}
}

func TestGateSourceEvaluatesNonCommandFactsAndToolModifiers(t *testing.T) {
	t.Parallel()
	boundaryRule, err := ParseRule([]byte(baseRule(`detection:
  sel:
    SocketCapability: durable
  condition: sel`)))
	if err != nil {
		t.Fatalf("ParseRule boundary: %v", err)
	}
	toolRule, err := ParseRule([]byte(`title: Native tool modifier
id: 1494cfa5-208d-41c4-865c-75cb4d8a3ea0
description: Matches a native write tool through a supported modifier.
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel:
    Tool|re: '^write$'
  condition: sel`))
	if err != nil {
		t.Fatalf("ParseRule tool: %v", err)
	}
	source := NewGateSource(NewMatcher(&Catalog{Packs: []Pack{{
		ID: "native", Enabled: true, Rules: []Rule{boundaryRule, toolRule},
	}}}))
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy},
},
Sockets: hitl.ActionSockets{
SocketGrants: []confine.SocketGrant{{ApprovedPath: "/tmp/db.sock", ResolvedPath: "/tmp/db.sock"}},
SocketScopes: []string{"durable"},
},
}
	match, ok := source.MatchAction(action, "strict")
	if !ok || match.RuleID == "" {
		t.Fatalf("non-command action was skipped: match=%+v ok=%v", match, ok)
	}
}

func TestApiActionNeverTransforms(t *testing.T) {
	t.Parallel()
	// Property: result always equals lower(a)+":"+lower(b) for aws <a> <b>.
	inputs := [][2]string{
		{"IAM", "CreateAccessKey"},
		{"s3", "DeleteBucket"},
		{"STS", "AssumeRole"},
		{"CreateSAMLProvider", "x"}, // nonsense pair still lower-copies
	}
	for _, pair := range inputs {
		argv := []string{"aws", pair[0], pair[1]}
		got := ApiActionFromArgv("aws", argv)
		want := strings.ToLower(pair[0] + ":" + pair[1])
		if got != want {
			t.Errorf("ApiActionFromArgv(aws, %v) = %q, want %q", argv, got, want)
		}
	}
}

func TestNewEgressEvent(t *testing.T) {
	t.Parallel()
	ev := NewEgressEvent(EgressObservation{
		DestinationHostname: "API.Stripe.COM.", DestinationPort: 443,
		DestinationIP: "203.0.113.8", Transport: "http_connect", Image: []string{"curl"},
		SessionID: "sess", ActionID: "action-1", Origin: "confined_proxy", DecisionStage: "pre_dial",
		RequestMethod: "post", RequestPath: "/v1/customers",
	})
	if ev.DestinationHostname != "api.stripe.com" || ev.DestinationPort != "443" {
		t.Fatalf("got host=%q port=%q", ev.DestinationHostname, ev.DestinationPort)
	}
	if ev.Initiated != "true" || !stringSlicesEqual(ev.Image, []string{"curl"}) || ev.SessionID != "sess" ||
		ev.DestinationIP != "203.0.113.8" || ev.Transport != "http_connect" || ev.ActionID != "action-1" ||
		ev.Origin != "confined_proxy" || ev.DecisionStage != "pre_dial" ||
		ev.RequestMethod != "POST" || ev.RequestPath != "/v1/customers" {
		t.Fatalf("unexpected fields: %+v", ev)
	}
	ev2 := NewEgressEvent(EgressObservation{DestinationHostname: "API.Example.COM", SessionID: "s"})
	if ev2.DestinationHostname != "api.example.com" || ev2.DestinationPort != "" {
		t.Fatalf("bare host: %+v", ev2)
	}
	if ev2.Initiated != "true" {
		t.Fatalf("Initiated = %q", ev2.Initiated)
	}
}

func TestEgressRuleMatchesHTTPRequestFields(t *testing.T) {
	t.Parallel()
	rule, err := ParseRule([]byte(`title: Protected API mutation
id: 40fc49b2-a87a-4eb9-b66f-49cd31f586d8
description: Matches one structured request before dial.
logsource: {product: lycaon, service: egress_observed}
level: high
detection:
  selection:
    RequestMethod: POST
    RequestPath|startswith: /v1/admin/
  condition: selection`))
	if err != nil || !rule.Supported {
		t.Fatalf("ParseRule: rule=%+v err=%v", rule, err)
	}
	event := NewEgressEvent(EgressObservation{
		DestinationHostname: "api.example.test", DestinationPort: 443,
		Transport: "http_connect", DecisionStage: "pre_dial",
		RequestMethod: "post", RequestPath: "/v1/admin/users",
	})
	if !rule.MatchesEgress(event) {
		t.Fatalf("request event did not match: %+v", event)
	}
	event.RequestPath = "/v1/public/users"
	if rule.MatchesEgress(event) {
		t.Fatal("unmatched request path satisfied the rule")
	}
}

func TestEgressRuleMatchesClosedMediatedFields(t *testing.T) {
	t.Parallel()
	r, err := ParseRule([]byte(`title: Exact mediated endpoint
id: 19887037-b755-4402-a0d4-9b98a990a2b9
description: Matches one exact pre-dial SOCKS endpoint observation.
logsource: {product: lycaon, service: egress_observed}
level: high
detection:
  sel:
    DestinationHostname: db.example.com
    DestinationPort: '5432'
    Transport: socks_tcp
    ActionId: action-7
    Origin: confined_proxy
    DecisionStage: pre_dial
  condition: sel`))
	if err != nil || !r.Supported {
		t.Fatalf("ParseRule: rule=%+v err=%v", r, err)
	}
	ev := NewEgressEvent(EgressObservation{
		DestinationHostname: "db.example.com", DestinationPort: 5432,
		Transport: "socks_tcp", SessionID: "s", ActionID: "action-7",
		Origin: "confined_proxy", DecisionStage: "pre_dial",
	})
	if !r.MatchesEgress(ev) {
		t.Fatalf("event did not match: %+v", ev)
	}
	ev.DecisionStage = "outcome"
	if r.MatchesEgress(ev) {
		t.Fatal("outcome event must not satisfy a pre-dial rule")
	}

	bad, err := ParseRule([]byte(`title: Invalid transport
id: f85b0878-977b-4535-b5e4-77ffbf3f39bd
description: Uses a transport outside the closed vocabulary.
logsource: {product: lycaon, service: egress_observed}
level: high
detection:
  sel: {Transport: raw_tcp}
  condition: sel`))
	if err != nil || bad.Supported || !strings.Contains(bad.UnsupportedReason, "unsupported value") {
		t.Fatalf("bad transport rule=%+v err=%v", bad, err)
	}
}

func TestEgressSourceAttributesCommandImagesAndCorrelatesByRuleHost(t *testing.T) {
	t.Parallel()
	rule, err := ParseRule([]byte(`title: AWS endpoint from wrapped command
id: 2a3fa86d-c92d-47bb-8732-0546e436ce35
description: Matches a mediated AWS endpoint from a wrapped command.
logsource:
  product: lycaon
  service: egress_observed
level: high
detection:
  selection:
    Image: aws
    DestinationHostname: iam.amazonaws.com
  condition: selection
`))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	source := NewEgressSource(NewMatcher(&Catalog{Packs: []Pack{{
		ID: "test-pack", Enabled: true, Rules: []Rule{rule},
	}}}))

	first, ok := source.Match(EgressObservation{
		DestinationHostname: "iam.amazonaws.com", DestinationPort: 443, Transport: "http_connect",
		SessionID: "chat-1", ActionID: "a1", Origin: "confined_proxy", DecisionStage: "pre_dial",
	}, "", "aws-vault exec prod -- aws iam list-users")
	if !ok || first.CorrelationID == "" {
		t.Fatalf("first match=%+v ok=%v", first, ok)
	}
	second, ok := source.Match(EgressObservation{
		DestinationHostname: "iam.amazonaws.com", DestinationPort: 443, Transport: "http_connect",
		SessionID: "chat-1", ActionID: "a2", Origin: "confined_proxy", DecisionStage: "pre_dial",
	}, "", "aws-vault exec prod -- aws iam create-user")
	if !ok || second.CorrelationID != first.CorrelationID {
		t.Fatalf("same alert family must correlate: first=%+v second=%+v ok=%v", first, second, ok)
	}
	third, ok := source.Match(EgressObservation{
		DestinationHostname: "iam.amazonaws.com", DestinationPort: 443, Transport: "http_connect",
		SessionID: "chat-2", ActionID: "a3", Origin: "confined_proxy", DecisionStage: "pre_dial",
	}, "", "aws iam list-users")
	if !ok || third.CorrelationID == first.CorrelationID {
		t.Fatalf("another chat must not correlate:first=%+v third=%+v ok=%v", first, third, ok)
	}
}

func TestGateSourceCorrelatesAlteredCommandsWithoutSharingAuthority(t *testing.T) {
	t.Parallel()
	rule, err := ParseRule([]byte(`title: Destructive AWS command
id: 3745e9e1-35a3-41e5-a6ca-e7f7d9eb2a29
description: Matches a destructive AWS command.
logsource:
  product: lycaon
  service: tool_exec
level: high
detection:
  selection:
    Tool: command
    CommandLine|contains: ' delete-'
  condition: selection
`))
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	source := NewGateSource(NewMatcher(&Catalog{Packs: []Pack{{
		ID: "test-pack", Enabled: true, Rules: []Rule{rule},
	}}}))
	action := func(chat, command string) hitl.ProposedAction {
		return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": command},
},
Scope: hitl.ActionScope{
ProjectDir: "/project",
SessionID: chat,
RootSessionID: chat,
},
}
	}
	firstAction := action("chat-1", "aws iam delete-user --user-name first")
	first, ok := source.MatchAction(firstAction, "strict")
	if !ok || first.CorrelationID == "" {
		t.Fatalf("first match=%+v ok=%v", first, ok)
	}
	secondAction := action("chat-1", "aws iam delete-role --role-name second")
	second, ok := source.MatchAction(secondAction, "strict")
	if !ok || second.CorrelationID != first.CorrelationID {
		t.Fatalf("same alert family must correlate: first=%+v second=%+v ok=%v", first, second, ok)
	}
	if hitl.GrantKey(firstAction) == hitl.GrantKey(secondAction) {
		t.Fatal("altered commands must retain distinct authorization identities")
	}
	third, ok := source.MatchAction(action("chat-2", "aws iam delete-user --user-name first"), "strict")
	if !ok || third.CorrelationID == first.CorrelationID {
		t.Fatalf("another chat must not correlate:first=%+v third=%+v ok=%v", first, third, ok)
	}
}

func TestLevelEscalates(t *testing.T) {
	t.Parallel()
	levels := []Level{LevelInformational, LevelLow, LevelMedium, LevelHigh, LevelCritical}
	postures := gate.Postures()
	for _, level := range levels {
		for _, posture := range postures {
			got := Escalates(level, posture)
			want := false
			switch level {
			case LevelCritical:
				want = true
			case LevelHigh:
				want = posture != "light"
			case LevelMedium:
				want = posture == "strict"
			case LevelInformational, LevelLow:
			}
			if got != want {
				t.Errorf("Escalates(%s, %s) = %v, want %v", level, posture, got, want)
			}
		}
	}
}

// Each posture enables a band the one below it does not have.
func TestPosturesAreDistinctBands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		level       string
		quiet, loud gate.Posture
	}{
		{"high", "light", "balanced"},
		{"medium", "balanced", "strict"},
	} {
		if Escalates(Level(tc.level), tc.quiet) {
			t.Errorf("%s must stay silent at %s", tc.level, tc.quiet)
		}
		if !Escalates(Level(tc.level), tc.loud) {
			t.Errorf("%s must escalate at %s", tc.level, tc.loud)
		}
	}
}

// A posture that bypassed parsing escalates as strict does, never as nothing.
func TestUnparsedPostureEscalatesAsStrict(t *testing.T) {
	t.Parallel()
	for _, posture := range []gate.Posture{"", "paranoid", "BALANCED"} {
		for _, level := range []Level{LevelInformational, LevelLow, LevelMedium, LevelHigh, LevelCritical} {
			if got, want := Escalates(level, posture), Escalates(level, gate.PostureStrict); got != want {
				t.Errorf("Escalates(%s, %q) = %v, want strict's %v", level, posture, got, want)
			}
		}
	}
}

func TestNewEventProjectsCapabilityBoundary(t *testing.T) {
	ev := NewEvent(ActionObservation{
		Tool:                 "command",
		CommandLine:          "ssh db.example.com",
		ProjectDir:           "/proj",
		SessionID:            "chat-1",
		ActionID:             "call-1",
		Boundary:             hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{"/proj"}, DirectIP: true, LoopbackAccess: true},
		Visibility:           "unobserved",
		DeclaredDestinations: []string{"db.example.com:22", "db.example.com:22"},
		Sockets: []SocketObservation{
			{ApprovedPath: "/tmp/z.sock", ResolvedPath: "/private/tmp/z.sock", Scope: "requested", GrantState: "new", EffectiveAuthority: "outside_sandbox_daemon"},
			{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/private/tmp/a.sock", Scope: "chat", GrantState: "existing", EffectiveAuthority: "outside_sandbox_daemon"},
		},
	})
	if ev.Contained != "false" || ev.FSJailed != "true" || ev.DirectIP != "true" {
		t.Fatalf("boundary projection = %+v", ev)
	}
	if ev.EgressMode != "direct_ip" || ev.Visibility != "unobserved" || ev.ActionID != "call-1" {
		t.Fatalf("direct projection = %+v", ev)
	}
	if ev.LoopbackAccess != "true" {
		t.Fatalf("LoopbackAccess=%q", ev.LoopbackAccess)
	}
	if ev.RootsDigest == "" || ev.SocketCount != "2" {
		t.Fatalf("bounded facts = %+v", ev)
	}
	if got := strings.Join(ev.SocketApprovedPath, ","); got != "/tmp/a.sock,/tmp/z.sock" {
		t.Fatalf("parallel socket sort = %q", got)
	}
	if ev.SocketCapability != "chat" || strings.Join(ev.SocketScope, ",") != "chat,requested" || strings.Join(ev.SocketGrantState, ",") != "existing,new" {
		t.Fatalf("socket scope/state projection = %+v", ev)
	}
	if len(ev.DeclaredDestination) != 1 {
		t.Fatalf("declared destinations = %+v", ev.DeclaredDestination)
	}
}

func testEvent(tool, command, projectDir string, contained bool, egressMode, sessionID string) Event {
	return testEventWithReach(tool, command, projectDir, contained, egressMode, sessionID, "")
}

func testEventWithReach(tool, command, projectDir string, contained bool, egressMode, sessionID, effectReach string) Event {
	return NewEvent(ActionObservation{
		Tool:        tool,
		CommandLine: command,
		ProjectDir:  projectDir,
		SessionID:   sessionID,
		Boundary:    hitl.Contained{FSJailed: contained, Egress: egressMode},
		EffectReach: effectReach,
	})
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
