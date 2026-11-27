package capabilitydrift

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/capabilitydrift/fixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/rejectinventory"
)

const fixturePackage = "./test/contract/internal/capabilitydrift/fixture"

var (
	restricted = fixture.HostState{ID: "team-sharing-off", Recipient: recipientProfile}
	admitted   = fixture.HostState{ID: "team-sharing-on", Recipient: recipientProfile, TeamSharing: true}
	shareCall  = api.ToolCall{ID: "call-share", Name: fixture.ToolName, Args: fixture.Args()}
)

// verdict is one check's outcome on one fixture state.
type verdict struct {
	Check    string   `json:"check"`
	Pass     bool     `json:"pass"`
	Findings []string `json:"findings,omitempty"`
}

// stageRecord is the qualification evidence for one fixture state.
type stageRecord struct {
	Stage     string                    `json:"stage"`
	Verdicts  []verdict                 `json:"verdicts"`
	Requests  []requestView             `json:"requests"`
	Receipts  []api.InvocationReceipt   `json:"receipts"`
	Witnesses []rejectinventory.Witness `json:"witnesses"`
}

type requestView struct {
	State       string   `json:"state"`
	Offered     []string `json:"offered"`
	HostMessage []string `json:"host_messages"`
}

func fixtureInventory(t *testing.T) rejectinventory.Inventory {
	t.Helper()
	inv, err := rejectinventory.Scan(filepath.Join(contractcheck.RepoRoot(t), "lycaon"), rejectinventory.Options{Patterns: []string{fixturePackage}})
	contractcheck.FailErr(t, "scan fixture emission sites", err)
	return inv
}

// discovery is the inventory check: every authority decision in the fixture
// has a witness that reached it.
func discovery(inv rejectinventory.Inventory, witnesses []rejectinventory.Witness) verdict {
	v := verdict{Check: "discovery", Pass: true}
	for _, f := range rejectinventory.UnwitnessedDecisions(inv, func(rejectinventory.Site) bool { return true }, witnesses) {
		v.Pass = false
		v.Findings = append(v.Findings, f.String())
	}
	return v
}

// guidanceCheck compares each captured first request's delivered instructions
// with the witnessed decisions for the same recipient and state.
func guidanceCheck(requests map[string]modelcall.CompletionRequest, witnesses []rejectinventory.Witness) verdict {
	v := verdict{Check: "guidance", Pass: true}
	known := func(name string) bool { return name == fixture.ToolName }
	states := make([]string, 0, len(requests))
	for state := range requests {
		states = append(states, state)
	}
	slices.Sort(states)
	for _, state := range states {
		for _, c := range Contradictions(DeliveredInstructions(recipientProfile, state, requests[state], known), witnesses) {
			v.Pass = false
			v.Findings = append(v.Findings, c.String())
		}
	}
	return v
}

func viewOf(state string, req modelcall.CompletionRequest) requestView {
	view := requestView{State: state}
	for _, meta := range req.Tools {
		view.Offered = append(view.Offered, meta.Name)
	}
	for _, msg := range req.Messages {
		if msg.Origin == api.MessageOriginHost {
			view.HostMessage = append(view.HostMessage, msg.Content)
		}
	}
	return view
}

// The discovery and guidance checks are qualified by one disposable handler
// whose owner restricts sharing with the team at request time. The handler,
// restriction, arguments and states stay fixed; only the witness set and the
// projection change. Stages 1 and 2 are the faulty fixture states the checks
// must reject, stage 3 is the corrected candidate, and the decline control
// shows a later decision does not contradict an offer.
func TestCapabilityDriftStages(t *testing.T) {
	inv := fixtureInventory(t)
	var records []stageRecord

	staleRestricted := runTurn(t, turn{state: restricted, projection: fixture.StaleProjection, calls: []api.ToolCall{shareCall}})
	witness, ok := witnessFrom(restricted, staleRestricted)
	if !ok {
		t.Fatalf("restricted call produced no witness; receipts %+v", staleRestricted.receipts)
	}
	staleRequests := map[string]modelcall.CompletionRequest{restricted.ID: staleRestricted.requests[0]}

	t.Run("stage1_restriction_without_witness", func(t *testing.T) {
		disc, guide := discovery(inv, nil), guidanceCheck(staleRequests, nil)
		records = append(records, stageRecord{Stage: "1", Verdicts: []verdict{disc, guide},
			Requests: []requestView{viewOf(restricted.ID, staleRestricted.requests[0])}, Receipts: staleRestricted.receipts})
		if disc.Pass {
			t.Error("discovery passed with an unwitnessed request-time restriction")
		} else if !slices.ContainsFunc(disc.Findings, func(f string) bool {
			return strings.Contains(f, "fixture/fixture.go") && strings.Contains(f, "fixture.Handler") && strings.Contains(f, fixture.RefusalCode)
		}) {
			t.Errorf("discovery findings do not name the restriction site: %v", disc.Findings)
		}
	})

	t.Run("stage2_witness_with_stale_projection", func(t *testing.T) {
		witnesses := []rejectinventory.Witness{witness}
		disc, guide := discovery(inv, witnesses), guidanceCheck(staleRequests, witnesses)
		records = append(records, stageRecord{Stage: "2", Verdicts: []verdict{disc, guide}, Witnesses: witnesses,
			Requests: []requestView{viewOf(restricted.ID, staleRestricted.requests[0])}, Receipts: staleRestricted.receipts})
		if !disc.Pass {
			t.Errorf("discovery failed with the restriction witnessed: %v", disc.Findings)
		}
		if guide.Pass {
			t.Error("guidance passed while the stale projection recommends a denied action")
		} else if !slices.ContainsFunc(guide.Findings, func(f string) bool {
			return strings.Contains(f, "recipient "+recipientProfile) && strings.Contains(f, "state "+restricted.ID) &&
				strings.Contains(f, fixture.ToolName+"(") && strings.Contains(f, witness.Decision) && strings.Contains(f, fixture.RefusalCode)
		}) {
			t.Errorf("guidance findings do not name instruction, action, recipient and decision: %v", guide.Findings)
		}
	})

	t.Run("stage3_projection_consumes_admission", func(t *testing.T) {
		repairedRestricted := runTurn(t, turn{state: restricted, projection: fixture.AdmittedProjection})
		repairedAdmitted := runTurn(t, turn{state: admitted, projection: fixture.AdmittedProjection, calls: []api.ToolCall{shareCall}})
		requests := map[string]modelcall.CompletionRequest{
			restricted.ID: repairedRestricted.requests[0], admitted.ID: repairedAdmitted.requests[0],
		}
		witnesses := []rejectinventory.Witness{witness}
		disc, guide := discovery(inv, witnesses), guidanceCheck(requests, witnesses)
		records = append(records, stageRecord{Stage: "3", Verdicts: []verdict{disc, guide}, Witnesses: witnesses,
			Requests: []requestView{viewOf(restricted.ID, requests[restricted.ID]), viewOf(admitted.ID, requests[admitted.ID])},
			Receipts: repairedAdmitted.receipts})
		if !disc.Pass || !guide.Pass {
			t.Errorf("corrected candidate failed: discovery %v guidance %v", disc.Findings, guide.Findings)
		}
		known := func(name string) bool { return name == fixture.ToolName }
		if got := DeliveredInstructions(recipientProfile, restricted.ID, requests[restricted.ID], known); len(got) != 0 || offers(requests[restricted.ID], fixture.ToolName) {
			t.Errorf("restricted request still advertises the action: instructions %v offered %v", got, viewOf(restricted.ID, requests[restricted.ID]).Offered)
		}
		if got := DeliveredInstructions(recipientProfile, admitted.ID, requests[admitted.ID], known); len(got) != 1 || !got[0].Offered {
			t.Errorf("admitted request lost the specific action: %v", got)
		}
		if len(repairedAdmitted.receipts) != 1 || repairedAdmitted.receipts[0].Status != api.InvocationStatusCompleted || !repairedAdmitted.receipts[0].Invoked {
			t.Errorf("admitted call did not complete through its owner: %+v", repairedAdmitted.receipts)
		}
	})

	t.Run("control_available_then_declined", func(t *testing.T) {
		declined := runTurn(t, turn{state: admitted, projection: fixture.AdmittedProjection, calls: []api.ToolCall{shareCall}, decline: true})
		if len(declined.receipts) != 1 || declined.receipts[0].Failure == nil {
			t.Fatalf("declined call settled without a refusal: %+v", declined.receipts)
		}
		r := declined.receipts[0]
		decline := rejectinventory.Witness{Tool: r.Tool, Recipient: recipientProfile, State: admitted.ID, Inputs: fixture.Args(),
			Code: r.Failure.Code, FailureClass: r.Failure.Class, Status: string(r.Status), Invoked: r.Invoked}
		witnesses := []rejectinventory.Witness{witness, decline}
		requests := map[string]modelcall.CompletionRequest{admitted.ID: declined.requests[0]}
		disc, guide := discovery(inv, witnesses), guidanceCheck(requests, witnesses)
		records = append(records, stageRecord{Stage: "control_declined", Verdicts: []verdict{disc, guide}, Witnesses: witnesses,
			Requests: []requestView{viewOf(admitted.ID, declined.requests[0])}, Receipts: declined.receipts})
		if !disc.Pass || !guide.Pass {
			t.Errorf("a later decline contradicted an earlier offer: discovery %v guidance %v", disc.Findings, guide.Findings)
		}
		if r.Invoked || r.Status != api.InvocationStatusRejected {
			t.Errorf("declined receipt = %+v, want a rejected receipt the owner never ran", r)
		}
	})

	writeEvidence(t, records)
}

func offers(req modelcall.CompletionRequest, tool string) bool {
	return slices.ContainsFunc(req.Tools, func(meta tools.ToolMeta) bool { return meta.Name == tool })
}

// writeEvidence logs each stage's verdicts and the full record as JSON, the
// qualification evidence archived with the run's receipt.
func writeEvidence(t *testing.T, records []stageRecord) {
	t.Helper()
	for _, r := range records {
		for _, v := range r.Verdicts {
			t.Logf("stage %s %s pass=%t findings=%v", r.Stage, v.Check, v.Pass, v.Findings)
		}
	}
	data, err := json.Marshal(records)
	contractcheck.FailErr(t, "marshal stage evidence", err)
	t.Logf("stage evidence: %s", data)
}
