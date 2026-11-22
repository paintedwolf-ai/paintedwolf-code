package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestShippedOARPairedStateRecoveryWitnesses(t *testing.T) {
	_, pipeline, _, _ := effectiveOARBlockPlane(t)

	callableCmdReach := toolreach.Freeze(
		[]string{"command", "ask_user", "fetch_url", "secret_generate"},
		toolsurface.Compile([]string{"command", "ask_user", "fetch_url", "secret_generate"}, nil),
	).WithPrograms(func(string) bool { return true })
	noCmdReach := toolreach.Freeze(
		[]string{"ask_user"},
		toolsurface.Compile([]string{"ask_user"}, nil),
	)
	noAskUserReach := toolreach.Freeze(
		[]string{"secret_generate"},
		toolsurface.Compile([]string{"secret_generate"}, nil),
	)

	t.Run("credential_literal_and_weak_credential_ask_user_secret_allowed", func(t *testing.T) {
		for _, tc := range []struct {
			code   string
			listed bool
			length int
			words  int
		}{
			{"CREDENTIAL_LITERAL", false, 24, 4},
			{"WEAK_CREDENTIAL_LITERAL", true, 8, 1},
		} {
			seq := 0
			evalFix := func(mode string, reach toolreach.Request) string {
				seq++
				ctx := tools.WithCallReach(t.Context(), reach)
				ctx = curationctx.WithSession(ctx, curationctx.Session{Mode: mode})
				gc := oar.NewGuardContext()
				gc.Session.SessionID = tc.code + "-" + mode
				gc.Session.SessionMode = mode
				gc.ObservationData = map[string]any{"assignment": "API_TOKEN"}
				gc.Published = map[string]any{
					"paintedwolf.assignment":                "API_TOKEN",
					"paintedwolf.credential_ignored":        false,
					"paintedwolf.credential_harvested":      false,
					"paintedwolf.listed":                    tc.listed,
					"paintedwolf.credential_length":         tc.length,
					"paintedwolf.credential_distinct_words": tc.words,
					"paintedwolf.credential_longest_run":    tc.length,
					"paintedwolf.credential_run_classes":    1,
					"paintedwolf.credential_fingerprint":    tc.code + "-" + mode + "-" + string(rune('a'+seq)),
				}
				tools.RegisterRecoveryFacts(ctx, gc)
				res, err := pipeline.EvaluateBlock(ctx, oar.AnchorCredentialAssignment, gc)
				contractcheck.FailErr(t, "EvaluateBlock "+tc.code, err)
				if res == nil || res.Decision == nil || res.Decision.Code != tc.code {
					t.Fatalf("expected %s decision, got %+v", tc.code, res)
				}
				return res.Decision.Copy["fix"]
			}
			agentText := evalFix("agent", callableCmdReach)
			if !strings.Contains(agentText, "ask_user") || !strings.Contains(agentText, "secret_generate") {
				t.Fatalf("%s agent+callable reach must offer ask_user and secret_generate: %q", tc.code, agentText)
			}
			askText := evalFix("ask", callableCmdReach)
			if strings.Contains(askText, "ask_user") || !strings.Contains(askText, "secret_generate") {
				t.Fatalf("%s ask mode must withhold secret ask_user while keeping secret_generate: %q", tc.code, askText)
			}
			absentText := evalFix("agent", noAskUserReach)
			if strings.Contains(absentText, "ask_user") || !strings.Contains(absentText, "secret_generate") {
				t.Fatalf("%s without ask_user must withhold ask_user: %q", tc.code, absentText)
			}
		}
	})

	t.Run("worker_url_not_observed_fetch_url_reach", func(t *testing.T) {
		evalWorker := func(sid string, reach toolreach.Request) string {
			ctx := tools.WithCallReach(t.Context(), reach)
			gc := oar.NewGuardContext()
			gc.Session.SessionID = sid
			gc.Session.Role = "worker"
			gc.Session.WorkerLeg = true
			gc.Grounding.WorkerSummaryPresent = true
			gc.Grounding.UnobservedCitedURLs = []string{"https://example.com/unseen"}
			gc.ObservationData = map[string]any{
				"offender_count":   1,
				"offenders_sample": "https://example.com/unseen",
			}
			tools.RegisterRecoveryFacts(ctx, gc)
			res, err := pipeline.EvaluateBlock(ctx, oar.AnchorWorkerReportCheck, gc)
			contractcheck.FailErr(t, "EvaluateBlock WORKER_URL_NOT_OBSERVED", err)
			if res == nil || res.Decision == nil || res.Decision.Code != "WORKER_URL_NOT_OBSERVED" {
				t.Fatalf("unexpected worker report decision: %+v", res)
			}
			return res.Decision.Copy["fix"]
		}
		withFetch := evalWorker("w-fetch-1", callableCmdReach)
		if !strings.Contains(withFetch, "fetch_url") {
			t.Fatalf("expected fetch_url recovery when fetch_url is callable: %q", withFetch)
		}
		withoutFetch := evalWorker("w-fetch-2", noCmdReach)
		if strings.Contains(withoutFetch, "fetch_url") || !strings.Contains(withoutFetch, "Drop the unobserved URLs") {
			t.Fatalf("expected drop-citation exit without fetch_url when absent: %q", withoutFetch)
		}
	})
}

func TestShippedOARCommandRecoveryAndPostureWitnesses(t *testing.T) {
	rs, _, _, bp := effectiveOARBlockPlane(t)
	callableCmdReach := toolreach.Freeze(
		[]string{"command", "ask_user"},
		toolsurface.Compile([]string{"command", "ask_user"}, nil),
	).WithPrograms(func(string) bool { return true })
	loadableCmdReach := toolreach.Freeze(
		[]string{"request_tools"},
		toolsurface.Compile([]string{"request_tools"}, []string{"command"}),
	).WithPrograms(func(string) bool { return true })
	noCmdReach := toolreach.Freeze(
		[]string{"ask_user"},
		toolsurface.Compile([]string{"ask_user"}, nil),
	)

	t.Run("command_not_argv_and_next_steps_command_remediations", func(t *testing.T) {
		rejectFix := func(tool, code string, reach toolreach.Request, data map[string]any) string {
			ctx := tools.WithCallReach(t.Context(), reach)
			err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
				Code: code, Data: data,
			})
			ref, ok := guidance.RefusalFromError(err)
			if !ok || ref == nil || ref.Code() != code {
				t.Fatalf("RejectObservation(%s) = %v, want %s", code, err, code)
			}
			return ref.Copy["fix"]
		}
		argvData := map[string]any{"command": "ls | head", "shell_metacharacters": true}
		if got := rejectFix("terminal_open", "COMMAND_NOT_ARGV", callableCmdReach, argvData); !strings.Contains(got, "`command(") {
			t.Fatalf("COMMAND_NOT_ARGV callable command missing command call: %q", got)
		}
		if got := rejectFix("terminal_open", "COMMAND_NOT_ARGV", loadableCmdReach, argvData); !strings.Contains(got, `request_tools(need="command")`) {
			t.Fatalf("COMMAND_NOT_ARGV loadable command missing request_tools step: %q", got)
		}
		if got := rejectFix("terminal_open", "COMMAND_NOT_ARGV", noCmdReach, argvData); strings.Contains(got, "request_tools") || strings.Contains(got, "request it, then run") || !strings.Contains(got, "not offered on this request") {
			t.Fatalf("COMMAND_NOT_ARGV without command/loader gave false promise: %q", got)
		}

		for _, tc := range []struct {
			tool string
			code string
			data map[string]any
		}{
			{"process_signal", "PROCESS_UNSUPPORTED", map[string]any{}},
			{"terminal_send", "REMOTE_PACKAGE_EXECUTION_REQUIRES_FRESH_BOUNDARY", map[string]any{"command": "npx create-vite app"}},
		} {
			avail := rejectFix(tc.tool, tc.code, callableCmdReach, tc.data)
			if !strings.Contains(avail, "`command(") {
				t.Fatalf("%s with callable command must render command next-step: %q", tc.code, avail)
			}
			unavail := rejectFix(tc.tool, tc.code, noCmdReach, tc.data)
			if strings.Contains(unavail, "`command(") || strings.Contains(unavail, "request_tools") {
				t.Fatalf("%s without command/loader must not promise command: %q", tc.code, unavail)
			}
		}
	})

	t.Run("can_command_copy_guards_and_spec_posture_bans", func(t *testing.T) {
		rejectCopy := func(tool, code string, reach toolreach.Request, data map[string]any) map[string]string {
			ctx := tools.WithCallReach(t.Context(), reach)
			err := bp.RejectObservation(ctx, tool, "coordinator", nil, &toolrejection.ToolReject{
				Code: code, Data: data,
			})
			ref, ok := guidance.RefusalFromError(err)
			if !ok || ref == nil || ref.Code() != code {
				t.Fatalf("RejectObservation(%s) = %v", code, err)
			}
			return ref.Copy
		}
		for _, tc := range []struct {
			tool    string
			code    string
			data    map[string]any
			wantCmd string
		}{
			{"write", "GIT_INTERNALS_WRITE_DENIED", map[string]any{"path": ".git/config", "class": "config"}, "git config --list"},
			{"code_rewrite", "SOURCE_PARSE_FAILED", map[string]any{"path": "a.go", "parse_language": "go", "parse_failure": "bad token"}, "sed -n"},
			{"write", "SECRET_REFERENCE_VALUE_UNSAFE", map[string]any{"path": ".env", "detail": "newline"}, "jq-edit --arg NAME VALUE"},
		} {
			withCmd := rejectCopy(tc.tool, tc.code, callableCmdReach, tc.data)
			if !strings.Contains(withCmd["fix"], tc.wantCmd) {
				t.Fatalf("%s with callable command missing %q: %q", tc.code, tc.wantCmd, withCmd["fix"])
			}
			withoutCmd := rejectCopy(tc.tool, tc.code, noCmdReach, tc.data)
			if strings.Contains(withoutCmd["fix"], tc.wantCmd) || strings.Contains(withoutCmd["instead"], tc.wantCmd) {
				t.Fatalf("%s without command leaked %q: fix=%q instead=%q", tc.code, tc.wantCmd, withoutCmd["fix"], withoutCmd["instead"])
			}
		}

		for _, specCode := range []string{
			"SPEC_POSTURE_STATE_FORBIDDEN",
			"SPEC_POSTURE_HANDOFF_FORBIDDEN",
			"SPEC_POSTURE_STUB_REQUIRED",
			"SPEC_POSTURE_UNRESOLVED",
		} {
			rule, ok := rs.Get(specCode)
			if !ok {
				t.Fatalf("missing %s", specCode)
			}
			if strings.Contains(rule.Copy.Fix, "command redirect") || strings.Contains(rule.Copy.Fix, "scoped patches") || strings.Contains(rule.Copy.Instead, "command redirect") {
				t.Fatalf("%s still promises build-only mutation in spec posture: fix=%q instead=%q", specCode, rule.Copy.Fix, rule.Copy.Instead)
			}
		}
		_ = api.SessionPostureSpec
	})
}
