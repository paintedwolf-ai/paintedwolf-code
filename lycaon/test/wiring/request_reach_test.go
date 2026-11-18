package wiring

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
)

// requestNames is the stock tool vocabulary the reach check reads names with.
func requestNames(t *testing.T) *toolvocab.Catalog {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "discover stock content", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "load tool profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load tool schemas", err)
	names, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "tool vocabulary", err)
	return names
}

// hostText is the text the host wrote into a message: host-origin or
// system-authority parts, or the whole message when it carries no parts.
func hostText(m api.Message) []string {
	host := func(origin api.MessageOrigin, authority api.ContentAuthority) bool {
		return origin == api.MessageOriginHost || authority == api.ContentAuthoritySystem || authority == api.ContentAuthorityDeveloper
	}
	if len(m.ContentParts) == 0 {
		if m.Role == api.MessageRoleSystem || host(m.Origin, m.Authority) {
			return []string{m.Content}
		}
		return nil
	}
	var out []string
	for _, part := range m.ContentParts {
		if host(part.Origin, part.Authority) {
			out = append(out, part.Content)
		}
	}
	return out
}

// requestReach lists what one recorded request names that it does not offer.
func requestReach(t *testing.T, names *toolvocab.Catalog, req modelcall.CompletionRequest, posture api.SessionPosture) []string {
	offered := map[string]bool{}
	for _, meta := range req.Tools {
		offered[meta.Name] = true
	}
	for marker := range commandReach(t, req.Debug.ProfileID, req.Debug.Surface, posture) {
		offered[marker] = true
	}
	var out []string
	for i, m := range req.Messages {
		for _, text := range hostText(m) {
			for _, name := range toolvocab.UnreachableNames(names, text, offered) {
				out = append(out, fmt.Sprintf("message %d (%s/%s/%s) names %s: %s", i, m.Role, m.Origin, m.Kind, name, lineWith(text, name)))
			}
		}
	}
	return out
}

// commandReach is what the executor admits through command for a profile on
// a surface in a posture, as the reach markers the vocabulary reads.
func commandReach(t *testing.T, profileID, surfaceID string, posture api.SessionPosture) map[string]bool {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	postures, err := sessionprofiles.LoadPostureRegistry()
	testutil.FailErr(t, "load postures", err)
	if posture == "" {
		posture = api.SessionPostureBuild
	}
	p, err := postures.Get(posture)
	testutil.FailErr(t, "posture", err)
	for i := range profiles {
		profiles[i].CommandEffects &= p.CommandEffects
	}
	if profileID == "" {
		profileID = prompts.CoordinatorProfileID
	}
	effects, err := surfacecatalog.AdmittedCommandEffects(profiles, profileID, surfaceID)
	testutil.FailErr(t, "admitted command effects", err)
	held := map[string]bool{}
	for class, markers := range map[toolcontract.CommandEffectClass][]string{
		toolcontract.CommandMutation:      {toolcontract.ReachCommandMutation},
		toolcontract.CommandExternalExec:  {toolcontract.ReachCommandExternalExec, toolcontract.ReachCommandExternalExecSelector},
		toolcontract.CommandNetworkEgress: {toolcontract.ReachCommandNetworkEgress},
	} {
		want, err := toolcontract.NewCommandEffects(class)
		testutil.FailErr(t, "command effect", err)
		if effects.Allows(want) {
			for _, m := range markers {
				held[m] = true
			}
		}
	}
	return held
}

func lineWith(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, name) {
			return fmt.Sprintf("%q", strings.TrimSpace(line))
		}
	}
	return "(no single line)"
}

// TestEveryRequestNamesOnlyWhatItOffers checks invariant P13 on the requests
// production sends: every name host text carries is in that request's own
// tools or reachable through its command language.
func TestEveryRequestNamesOnlyWhatItOffers(t *testing.T) {
	names := requestNames(t)
	for label, decider := range map[string]decide.Decider{"load-all": decidetest.LoadAll{}, "absent": decide.Absent{}} {
		t.Run(label, func(t *testing.T) { probeRequests(t, names, decider) })
	}
}

func probeRequests(t *testing.T, names *toolvocab.Catalog, decider decide.Decider) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "parallel modules",
			ToolCalls:    []llm.MockToolCall{{ID: "t1", Name: "task", Args: TaskToolArgs("implementer", "Add module scaffold under pkg/a", "pkg/a/**")}},
			FollowUpText: "Dispatched implementer for module A.",
		},
		{Pattern: "what is here", FollowUpText: "A small Go module."},
	}})
	rec := llm.NewRecordingClient(mock)
	h := BuildForTest(t, WithLLMClient(rec), WithDecider(decider))
	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	for _, prompt := range []string{"what is here", "build parallel modules with implementer"} {
		if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, prompt); err != nil {
			testutil.FailErr(t, "prompt", err)
		}
	}
	var violations []string
	for i, req := range rec.AllRequests() {
		tools := make([]string, 0, len(req.Tools))
		for _, meta := range req.Tools {
			if meta.Deferred {
				tools = append(tools, meta.Name+"(deferred)")
			} else {
				tools = append(tools, meta.Name)
			}
		}
		sort.Strings(tools)
		t.Logf("request %d surface=%q profile=%q iter=%d composition=%q tooluse=%d tools=%v messages=%d", i, req.Debug.Surface, req.Debug.ProfileID, req.Debug.Iteration, req.Composition, req.ToolUse, tools, len(req.Messages))
		for j, m := range req.Messages {
			head := m.Content
			if len(head) > 300 {
				head = head[:300]
			}
			t.Logf("  msg %d role=%s origin=%s kind=%s authority=%s parts=%d len=%d host=%d %q", j, m.Role, m.Origin, m.Kind, m.Authority, len(m.ContentParts), len(m.Content), len(hostText(m)), head)
		}
		if req.Debug.Surface == "implement_park" {
			t.Logf("PARK SYSTEM PROMPT:\n%s\nEND PARK", req.Messages[0].Content)
		}
		for _, v := range requestReach(t, names, req, api.SessionPostureBuild) {
			violations = append(violations, fmt.Sprintf("request %d: %s", i, v))
		}
	}
	slices.Sort(violations)
	if len(violations) > 0 {
		t.Errorf("%d names not offered:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
