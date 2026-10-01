package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
)

// Gather nearby endpoint requests from the same command into one review.
const egressGatherWindow = 500 * time.Millisecond

// egressGatherCap bounds one observed set. Beyond it, endpoints ask alone.
const egressGatherCap = 32

// DeclaredEndpointObserved marks a destination set the broker observed while a
// command ran, as opposed to one a provider catalog declared.
const DeclaredEndpointObserved = "observed"

// egressRaise is everything one parked endpoint needs to raise its card.
type egressRaise struct {
	cmd       confine.EgressCommand
	ep        egressproxy.Endpoint
	action    hitl.ProposedAction
	subject   egressAskSubject
	decision  *gate.Decision
	facts     gate.Facts
	detection *hitl.DetectionMatch
	matches   []hitl.ApprovalRuleMatch
}

type egressGatherKey struct{ chat, toolCallID string }

// egressGather is one command's open window. Members join until the window
// closes; the first member past the window raises the card for all of them.
type egressGather struct {
	mu      sync.Mutex
	members []egressRaise
	closed  bool
	ready   chan struct{}
	done    chan struct{}
	lead    sync.Once
	allowed bool
	final   *hitl.CheckpointResponse
}

// egressGathers holds the executor's open coalescing windows.
type egressGathers struct {
	mu   sync.Mutex
	open map[egressGatherKey]*egressGather
}

func (e *DefaultToolExecutor) gathers() *egressGathers {
	e.egressGatherOnce.Do(func() {
		e.egressGather = &egressGathers{
			open: map[egressGatherKey]*egressGather{},
		}
	})
	return e.egressGather
}

// join adds a member to the command's open window, opening one when none is
// open. leader is true for the member that opened it.
func (g *egressGathers) join(key egressGatherKey, member egressRaise) (*egressGather, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if open := g.open[key]; open != nil {
		open.mu.Lock()
		if !open.closed && len(open.members) < egressGatherCap {
			open.members = append(open.members, member)
			open.mu.Unlock()
			return open, false
		}
		open.mu.Unlock()
	}
	gather := &egressGather{members: []egressRaise{member}, ready: make(chan struct{}), done: make(chan struct{})}
	g.open[key] = gather
	time.AfterFunc(egressGatherWindow, func() {
		gather.mu.Lock()
		gather.closed = true
		gather.mu.Unlock()
		g.mu.Lock()
		if g.open[key] == gather {
			delete(g.open, key)
		}
		g.mu.Unlock()
		close(gather.ready)
	})
	return gather, true
}

// Gather ordinary endpoints; detections and declared sets retain their exact review identities.
func (e *DefaultToolExecutor) gatherEgress(ctx context.Context, raise egressRaise) (allowed, handled bool) {
	toolCallID := strings.TrimSpace(raise.cmd.ToolCallID)
	if toolCallID == "" || raise.detection != nil || raise.subject.Declared != nil {
		return false, false
	}
	key := egressGatherKey{chat: raise.action.ChatSession(), toolCallID: toolCallID}
	gather, _ := e.gathers().join(key, raise)
	select {
	case <-gather.ready:
	case <-ctx.Done():
		return false, true
	}
	gather.lead.Do(func() {
		gather.mu.Lock()
		members := append([]egressRaise(nil), gather.members...)
		gather.mu.Unlock()
		var final *hitl.CheckpointResponse
		if len(members) == 1 {
			final = e.raiseEgressCard(ctx, members[0])
		} else {
			final = e.raiseEgressSetCard(ctx, members)
		}
		gather.final = final
		gather.allowed = hitl.CheckpointAuthorizes(final)
		close(gather.done)
	})
	select {
	case <-gather.done:
	case <-ctx.Done():
		return false, true
	}
	e.settleEgressMember(ctx, raise, gather.allowed, gather.final)
	return gather.allowed, true
}

// settleEgressMember records the outcome for one endpoint of a decided card.
func (e *DefaultToolExecutor) settleEgressMember(ctx context.Context, raise egressRaise, allowed bool, final *hitl.CheckpointResponse) {
	if allowed {
		e.recordHostVisit(ctx, raise.action.ChatSession(), raise.ep.Host)
		return
	}
	e.rememberCapabilityDenial(raise.cmd.SessionID, raise.cmd.ToolCallID, final)
}

// Posture and the reviewed boundary determine widening offers.
func egressWidening(raise egressRaise) (face bool, menu bool) {
	if raise.detection != nil || raise.facts.SecretExposed || raise.subject.Declared != nil {
		return false, false
	}
	posture := raise.decision.Posture
	if posture.Widens() {
		return true, false
	}
	return false, posture.Ladder().Widening == gate.WidenMenuOnly
}

// Posture determines whether command-network authority is primary or a menu choice.
func (e *DefaultToolExecutor) egressOffers(raise egressRaise) []hitl.ApprovalGrantOffer {
	offers := e.grantOffers(raise.action, &hitl.ApprovalResult{Decision: raise.decision})
	command, ok := hitl.CommandNetworkOffer(raise.action)
	if !ok {
		return offers
	}
	face, menu := egressWidening(raise)
	switch {
	case face:
		return replaceChatSlot(offers, command)
	case menu:
		command.Group = hitl.GroupAlsoAllow
		return append(offers, command)
	}
	return offers
}

// Preserve the primary chat rung's position when replacing its offer.
func replaceChatSlot(offers []hitl.ApprovalGrantOffer, wider hitl.ApprovalGrantOffer) []hitl.ApprovalGrantOffer {
	out := make([]hitl.ApprovalGrantOffer, 0, len(offers)+1)
	replaced := false
	for _, offer := range offers {
		if !replaced && offer.Rung == hitl.ApprovalRungChat && strings.TrimSpace(offer.Group) == "" {
			out = append(out, wider)
			replaced = true
			continue
		}
		out = append(out, offer)
	}
	if !replaced {
		out = append(out, wider)
	}
	return out
}

// raiseEgressCard raises the single-endpoint card.
func (e *DefaultToolExecutor) raiseEgressCard(ctx context.Context, raise egressRaise) *hitl.CheckpointResponse {
	offers := e.egressOffers(raise)
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 raise.action,
		Title:                  raise.subject.Title,
		ToolCallID:             raise.cmd.ToolCallID,
		ProjectID:              raise.cmd.ProjectID,
		ApprovalMatches:        raise.matches,
		Decision:               raise.decision,
		Explanation:            raise.subject.Explanation,
		Detection:              raise.detection,
		DeclaredEndpoints:      raise.subject.Declared,
		GrantOffers:            offers,
		SkipGrantOfferAutofill: true,
		// The ledger records the attempted endpoint, even when the card covers a destination set.
		DetectionEndpoints: []authzledger.CapabilityEndpoint{{
			Host: raise.ep.Host, Port: raise.ep.Port, Transport: string(raise.ep.Transport), Allowed: true, Attempts: 1,
		}},
		CoalesceKey: raise.subject.ApprovalKey,
	})
	if err != nil {
		return nil
	}
	return final
}

// A gathered card grants only the host-port pairs observed in its window.
func (e *DefaultToolExecutor) raiseEgressSetCard(ctx context.Context, members []egressRaise) *hitl.CheckpointResponse {
	sort.SliceStable(members, func(i, j int) bool {
		return endpointLabel(members[i].ep) < endpointLabel(members[j].ep)
	})
	lead := members[0]
	labels := make([]string, 0, len(members))
	keys := make([]string, 0, len(members))
	endpoints := make([]authzledger.CapabilityEndpoint, 0, len(members))
	for _, m := range members {
		labels = append(labels, endpointLabel(m.ep))
		key := hitl.GrantKey(m.action)
		if key == "" {
			return nil
		}
		keys = append(keys, key)
		endpoints = append(endpoints, authzledger.CapabilityEndpoint{
			Host: m.ep.Host, Port: m.ep.Port, Transport: string(m.ep.Transport), Allowed: true, Attempts: 1,
		})
	}
	digestBytes := sha256.Sum256([]byte(strings.Join(labels, "\x00")))
	digest := base64.RawURLEncoding.EncodeToString(digestBytes[:16])
	count := len(labels)
	noun := strconv.Itoa(count) + " hosts"

	action := hitl.ProposedAction{
		Tool:             networkEgressTool,
		PresentationTool: lead.action.PresentationTool,
		Args:             map[string]any{"hosts": labels, "host_count": count, "observed_hosts_digest": digest},
		Command:          lead.action.Command,
		EstimatedImpact:  "Outbound connections to the " + noun + " this command reached",
		ProjectID:        lead.action.ProjectID,
		ProjectDir:       lead.action.ProjectDir,
		SessionID:        lead.action.SessionID,
		RootSessionID:    lead.action.RootSessionID,
		Contained:        lead.action.Contained,
	}
	explanation := &hitl.ApprovalExplanation{
		What:      "Open tunnels to the " + noun + " this command reached while running",
		Who:       hitl.WhoAgentCommand + " over mediated TCP",
		IfWrong:   "The tunnels' contents are not visible to the app — they could carry anything to these destinations.",
		AllowLine: "connections to these " + noun + " on the ports shown",
	}
	set := egressRaise{
		cmd: lead.cmd, ep: lead.ep, action: action, decision: lead.decision, facts: lead.facts, matches: lead.matches,
		subject: egressAskSubject{
			Args: action.Args, Title: "Allow network: " + noun, Impact: action.EstimatedImpact, Explanation: explanation,
			Contained: action.Contained, ApprovalKey: "egress-observed:" + digest,
			Declared: &hitl.DeclaredEndpoints{Hosts: labels, HostCount: count, Digest: digest, Source: DeclaredEndpointObserved},
		},
	}
	offers := observedSetOffers(action, keys, lead.decision)
	if command, ok := hitl.CommandNetworkOffer(action); ok {
		// The gathered endpoints share one command's widening decision.
		widen := lead.detection == nil && !lead.facts.SecretExposed && lead.decision.Posture.Widens()
		switch {
		case widen:
			offers = replaceChatSlot(offers, command)
		case lead.decision.Posture.Ladder().Widening == gate.WidenMenuOnly:
			command.Group = hitl.GroupAlsoAllow
			offers = append(offers, command)
		}
	}
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 set.action,
		Title:                  set.subject.Title,
		ToolCallID:             set.cmd.ToolCallID,
		ProjectID:              set.cmd.ProjectID,
		ApprovalMatches:        set.matches,
		Decision:               set.decision,
		Explanation:            set.subject.Explanation,
		DeclaredEndpoints:      set.subject.Declared,
		GrantOffers:            offers,
		SkipGrantOfferAutofill: true,
		DetectionEndpoints:     endpoints,
		CoalesceKey:            set.subject.ApprovalKey,
	})
	if err != nil {
		return nil
	}
	return final
}

// observedSetOffers is the set card's ladder: day and chat over the exact
// member endpoints, and the project slot over the same set.
func observedSetOffers(action hitl.ProposedAction, keys []string, decision *gate.Decision) []hitl.ApprovalGrantOffer {
	reuse := decision.Reuse()
	task := hitl.ExactActionSetOffer(action, keys)
	day := hitl.DayRung(task)
	project := hitl.ExactActionSetOfferAtScope(action, keys, hitl.ApprovalGrantScopeProject)
	switch {
	case !action.HasProjectIdentity():
		project = hitl.DisabledOffer(project, hitl.NoteNoProjectOpen)
	case reuse.Scope == gate.ScopeChat:
		project = hitl.DisabledOffer(project, hitl.NoteEndsWithChat)
	}
	return []hitl.ApprovalGrantOffer{day, task, project}
}

func endpointLabel(ep egressproxy.Endpoint) string {
	if ep.Port == 0 {
		return ep.Host
	}
	return fmt.Sprintf("%s:%d", ep.Host, ep.Port)
}
