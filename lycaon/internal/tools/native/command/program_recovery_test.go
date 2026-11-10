package command_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommandRecoveryPreservesUnitAndPriorEffects(t *testing.T) {
	for _, body := range []string{"printf '%s' 'open", "if true; then echo ok", "cat <<'END'\nliteral\n"} {
		t.Run(body, func(t *testing.T) {
			f := newRecoveryFixture(t)
			_, err := f.call(t, t.Context(), "printf before > retained; "+body)
			rejected := toolrejection.AsToolReject(err)
			if rejected == nil || rejected.Code != "COMMAND_SYNTAX_INVALID" || rejected.Data["command_execution"] != "not_started" {
				t.Fatalf("top-level refusal: %#v / %v", rejected, err)
			}
			if _, err := os.Stat(filepath.Join(f.Dir, "retained")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid unit executed: %v", err)
			}
		})
	}
	f := newRecoveryFixture(t)
	testutil.FailErr(t, "set write progress", f.SetProgress("- [ ] Retain the write before inspecting source admission"))
	out, err := f.call(t, t.Context(), `printf before | tee retained; eval 'if'`)
	testutil.FailErr(t, "run captured invalid source", err)
	got := decodeOutcome(t, out)
	if got.Refusal == nil || got.Refusal.Details["diagnostic_top_level"] != false || got.Refusal.Details["command_execution"] != "observed" || strings.Contains(got.Refusal.Feedback, "No command operation ran") {
		t.Fatalf("captured refusal lost prior effects: %+v / %+v", got, got.Refusal)
	}
	retained, err := os.ReadFile(filepath.Join(f.Dir, "retained"))
	testutil.FailErr(t, "read retained effect", err)
	if string(retained) != "before" || got.CompletedEffects < 1 || got.Refusal.Details["parser_kind"] == nil {
		t.Fatalf("lost completed write or parser state: %+v / %q", got, retained)
	}
	if !strings.Contains(got.Stderr, "if") || strings.Contains(got.Stderr, "encoding") {
		t.Fatalf("shell diagnostic did not name parse problem: %q", got.Stderr)
	}
}

func TestCommandRecoveryKeepsNestedSourceIdentitiesDistinct(t *testing.T) {
	f := newRecoveryFixture(t)
	out, err := f.call(t, t.Context(), `eval 'eval "if"'`)
	testutil.FailErr(t, "run nested invalid source", err)
	got := decodeOutcome(t, out)
	if got.Refusal == nil {
		t.Fatalf("missing refusal: %+v", got)
	}
	facts := got.Refusal.Details
	primary, _ := facts["source_unit_id"].(string)
	enclosing, _ := facts["operation_source_unit_id"].(string)
	if primary == "" || enclosing == "" || primary == enclosing || enclosing == "submitted" || facts["diagnostic_top_level"] != false {
		t.Fatalf("nested source provenance collapsed: %#v", facts)
	}
}

func newRecoveryFixture(t *testing.T) *programFixture {
	t.Helper()
	f := newProgramFixture(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	return f
}

func TestCommandInputIsExplicitAndPreservesLiteralBytes(t *testing.T) {
	f := newRecoveryFixture(t)
	for _, tc := range []struct{ source, want string }{
		{`if read -r value; then printf unexpected; else printf eof; fi`, "eof"},
		{"read -r value <<'INPUT'\nit's literal $HOME\nINPUT\nprintf '%s' \"$value\"", "it's literal $HOME"},
	} {
		out, err := f.call(t, t.Context(), tc.source)
		testutil.FailErr(t, "run explicit input", err)
		got := decodeOutcome(t, out)
		if got.Status != 0 || got.Refusal != nil || got.Stdout != tc.want {
			t.Fatalf("stdin contract changed: %+v, want %q", got, tc.want)
		}
	}
}

func TestReachedRecoveryTravelsFromLedgerToModel(t *testing.T) {
	f := newRecoveryFixture(t)
	var parentID, callID string
	observed := &tools.ToolInvocationOut{}
	f.Configure = func(tc *tools.ToolContext) {
		parentID, callID = tc.Invocation.ID, tc.Identity.ToolCallID
		tc.Effects.Out = observed
	}
	testutil.FailErr(t, "establish write progress", f.SetProgress("- [ ] Preserve output and repair the source"))
	body, err := f.call(t, t.Context(), `printf before | tee retained; eval 'if'`)
	testutil.FailErr(t, "run reached refusal", err)
	receipt, err := f.Recorder.Settle(t.Context(), parentID, invocation.Settlement{Status: api.InvocationStatusCompleted, Invoked: true, EvidenceKind: "program", SourceVerdict: api.SourceVerdictFailed})
	testutil.FailErr(t, "settle model invocation", err)
	message := api.Message{ID: "retained-result", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: body, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: callID, Content: body, Invocation: receipt, CommandProgram: observed.CommandProgram}}
	projected := transcript.Project([]api.Message{message})
	text := projected[len(projected)-1].Content
	for _, want := range []string{"COMMAND_SYNTAX_INVALID", "before"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reached recovery lost %q: %s", want, text)
		}
	}
	if strings.Contains(text, `"feedback"`) || message.Content != body {
		t.Fatalf("recovery contradicted or rewrote retained results: %s", text)
	}
}

func newCurlRecoveryFixture(t *testing.T, ownedPorts ...uint16) *programFixture {
	t.Helper()
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	testutil.FailErr(t, "compile profile effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID:             "fixture",
		Tools:          map[string]bool{"command": true, "http_request": true, "read": true, "write": true},
		CommandEffects: classes,
	}})
	f := &programFixture{programtest.New(t, programtest.Options{
		Tools: []string{"http_request"},
		Owners: func(ports programtest.OwnerPorts) func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
			curlOp := command.NativeOperation{Tool: "http_request", Prepare: func(_ context.Context, _ *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				bound, err := commandprogram.CatalogBinder{}.Bind(catalogSpec("curl"), request.Argv)
				if err != nil {
					return command.NativePrepared{}, err
				}
				operands := bound.Operands()
				targetURL := operands[len(operands)-1].Value
				return command.NativePrepared{
					Mode:   "request",
					Review: bound.Review("http_request"),
					Invoke: func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						tc.Local.OwnedLoopbackPorts = append([]uint16(nil), ownedPorts...)
						ex, err := httpaction.Perform(ctx, httpaction.Deps{Boundary: boundary}, map[string]any{"url": targetURL, "redirects": "safe"}, tc, httpaction.ExchangeOptions{})
						if err != nil {
							certainty := toolcontract.NoEffect
							var exchangeErr *outboundhttp.ExchangeError
							if errors.As(err, &exchangeErr) {
								switch {
								case exchangeErr.RequestsStarted > exchangeErr.ResponsesReceived:
									certainty = toolcontract.EffectUnknown
								case exchangeErr.ResponsesReceived > 0:
									certainty = toolcontract.EffectCompleted
								}
							}
							if reject := toolrejection.AsToolReject(err); reject != nil && (reject.Code == "HTTP_REQUEST_HOST_DENIED" || reject.Code == "SANDBOX_TRY_LOOPBACK_CONNECT") {
								return toolcontract.ProducerResult{Status: 7, Terminal: toolcontract.ProducerRefused, StopCode: reject.Code, EffectCertainty: certainty}, err
							}
							return toolcontract.ProducerResult{Status: 52, Terminal: toolcontract.ProducerFailed, EffectCertainty: certainty}, err
						}
						_, _ = streams.Output.Write(ex.Response.Body)
						return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
					},
				}, nil
			}}
			return func(tools.ToolContext, *projectpaths.ProgramPaths, *toolcontract.ProgramMemory, tooloutput.SpillAdmission) (command.ProgramOwners, error) {
				return command.ProgramOwners{Natives: map[string]command.NativeOperation{"curl": curlOp, "tee": teeOperation(ports.Dir)}}, nil
			}
		},
	})}
	f.Boundary = boundary
	f.Runtime.Boundary = boundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), f.Runtime.Registry, "fixture")
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	f.Configure = func(tc *tools.ToolContext) {
		tc.Local.OwnedLoopbackPorts = append([]uint16(nil), ownedPorts...)
	}
	return f
}

func recoveryServerPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	testutil.FailErr(t, "split server host:port", err)
	port, err := strconv.ParseUint(portText, 10, 16)
	testutil.FailErr(t, "parse server port", err)
	return uint16(port)
}

func TestCommandProgramCurlRedirectPartialOutcomeAndNoAutoReplay(t *testing.T) {
	var originHits, targetHits atomic.Int32
	dropHop1AfterRequest := atomic.Bool{}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		if dropHop1AfterRequest.Load() {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("target response writer does not support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = io.WriteString(w, "landed\n")
	}))
	t.Cleanup(target.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		http.Redirect(w, r, target.URL+"/landed", http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	ownedPorts := []uint16{recoveryServerPort(t, origin.URL), recoveryServerPort(t, target.URL)}

	var mu sync.Mutex
	var asked []string
	allow := map[string]bool{}
	sessionGrants := map[string]bool{}
	confine.SetEgressPosture(confine.PostureAsk)
	release := confine.SetEgressResolver(func(_ context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		mu.Lock()
		defer mu.Unlock()
		endpoint := net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port))
		grantKey := cmd.SessionID + "|" + endpoint
		if sessionGrants[grantKey] {
			return true
		}
		asked = append(asked, endpoint)
		if allow[endpoint] {
			sessionGrants[grantKey] = true
			return true
		}
		return false
	})
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		_ = release(context.Background())
	})

	originHost := strings.TrimPrefix(origin.URL, "http://")
	targetHost := strings.TrimPrefix(target.URL, "http://")
	allow[originHost] = true

	// 1. Denied redirect hop after Hop 0 completed truthfully reports completed initial hop effects
	// (State=="rejected", CompletedEffects==1, ReplayAvailable==false, command_execution=="observed", program_replay_available==false)
	// and never sends a connection or request to the denied redirect target.
	fDeny := newCurlRecoveryFixture(t, ownedPorts...)
	raw, err := fDeny.call(t, t.Context(), fmt.Sprintf("curl -s -L '%s'", origin.URL))
	testutil.FailErr(t, "run curl redirect with denied Hop 1", err)
	gotDeny := decodeOutcome(t, raw)
	if gotDeny.State != "rejected" || gotDeny.CompletedEffects != 1 || gotDeny.ReplayAvailable || gotDeny.Refusal == nil || gotDeny.Refusal.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("denied redirect hop program outcome = %+v / refusal=%+v", gotDeny, gotDeny.Refusal)
	}
	if originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("denied redirect hop hits: origin=%d target=%d, want 1/0", originHits.Load(), targetHits.Load())
	}
	details := gotDeny.Refusal.Details
	if details["command_execution"] != "observed" || details["program_completed_effects"] != float64(1) || details["program_replay_available"] != false ||
		details["http_requests_started"] != float64(1) || details["http_responses_received"] != float64(1) {
		t.Fatalf("denied redirect hop refusal details lost completed initial hop state: %#v", details)
	}
	if !strings.Contains(gotDeny.Refusal.Feedback, "Earlier exchange attempts have already started (1 attempts, 1 responses).") ||
		strings.Contains(gotDeny.Refusal.Feedback, "No command operation ran") {
		t.Fatalf("denied redirect hop feedback misrepresented completed initial hop: %q", gotDeny.Refusal.Feedback)
	}

	// 2. Unknown post-send outcome on Hop 1 settles with UncertainEffects==1 and ReplayAvailable==false, without auto-replaying Hop 0 or Hop 1.
	originHits.Store(0)
	targetHits.Store(0)
	dropHop1AfterRequest.Store(true)
	mu.Lock()
	allow[targetHost] = true
	asked = nil
	sessionGrants = map[string]bool{}
	mu.Unlock()

	fUnknown := newCurlRecoveryFixture(t, ownedPorts...)
	raw, err = fUnknown.call(t, t.Context(), fmt.Sprintf("curl -s -L '%s'", origin.URL))
	testutil.FailErr(t, "run curl redirect with unknown Hop 1 outcome", err)
	gotUnknown := decodeOutcome(t, raw)
	if gotUnknown.UncertainEffects != 1 || gotUnknown.ReplayAvailable || originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("unknown Hop 1 outcome = %+v originHits=%d targetHits=%d, want UncertainEffects=1 ReplayAvailable=false and 1/1 hits (no auto-replay)",
			gotUnknown, originHits.Load(), targetHits.Load())
	}

	// 3. Authorized redirect chain and repeat call succeed without re-asking for already-granted hosts.
	originHits.Store(0)
	targetHits.Store(0)
	dropHop1AfterRequest.Store(false)
	mu.Lock()
	asked = nil
	sessionGrants = map[string]bool{}
	mu.Unlock()

	fAllow := newCurlRecoveryFixture(t, ownedPorts...)
	raw, err = fAllow.call(t, t.Context(), fmt.Sprintf("curl -s -L '%s'", origin.URL))
	testutil.FailErr(t, "run authorized curl redirect chain", err)
	gotAllow := decodeOutcome(t, raw)
	if gotAllow.State != "completed" || gotAllow.Status != 0 || gotAllow.Stdout != "landed\n" || originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("authorized redirect chain outcome = %+v originHits=%d targetHits=%d", gotAllow, originHits.Load(), targetHits.Load())
	}
	mu.Lock()
	firstAsked := len(asked)
	asked = nil
	mu.Unlock()
	if firstAsked != 2 {
		t.Fatalf("first authorized redirect chain asked %d times, want 2", firstAsked)
	}

	raw, err = fAllow.call(t, t.Context(), fmt.Sprintf("curl -s -L '%s'", origin.URL))
	testutil.FailErr(t, "repeat authorized curl redirect chain", err)
	gotRepeat := decodeOutcome(t, raw)
	mu.Lock()
	repeatAsked := len(asked)
	mu.Unlock()
	if gotRepeat.State != "completed" || gotRepeat.Status != 0 || gotRepeat.Stdout != "landed\n" || repeatAsked != 0 {
		t.Fatalf("repeat redirect chain outcome = %+v repeatAsked=%d", gotRepeat, repeatAsked)
	}
}

