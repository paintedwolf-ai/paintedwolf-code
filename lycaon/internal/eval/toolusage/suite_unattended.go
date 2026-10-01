package toolusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// UnattendedPolicy supplies fixed responses. Sandbox fixtures may approve only
// their exact ephemeral resource through a host-authored one-invocation option.
type UnattendedPolicy struct {
	ApprovalGuidance string `yaml:"approval_guidance" json:"approval_guidance"`
	Answer           string `yaml:"answer" json:"answer"`
	MaxInterventions int    `yaml:"max_interventions" json:"max_interventions"`
}

func (p *UnattendedPolicy) validate() error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.ApprovalGuidance) == "" || strings.TrimSpace(p.Answer) == "" || p.MaxInterventions < 1 || p.MaxInterventions > 10 {
		return fmt.Errorf("unattended policy requires denial guidance, an answer, and 1–10 interventions")
	}
	return nil
}

type AutomaticResponse struct {
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
}

type interactionExhausted struct{}

func (interactionExhausted) Error() string {
	return "the task could not proceed within its declared unattended interaction policy"
}

const (
	approvalRungOnce = "once"
	approvalRungTask = "task"
)

type scriptedOperator struct {
	client        *liveClient
	result        *CaseReport
	policy        UnattendedPolicy
	rules         []ApprovalRule
	responded     map[string]bool
	ruleDenials   map[string]int
	interventions int
}

func (c *liveClient) unattendedObserver(ctx context.Context, result *CaseReport, policy UnattendedPolicy, rules []ApprovalRule, progress func(CaseReport) error) func([]wire.CheckpointEvent, *FeedbackRequest) error {
	observe := observeCaseHumanInput(result, true, progress)
	operator := scriptedOperator{client: c, result: result, policy: policy, rules: rules, responded: map[string]bool{}, ruleDenials: map[string]int{}}
	return func(checkpoints []wire.CheckpointEvent, feedback *FeedbackRequest) error {
		if err := observe(checkpoints, feedback); err != nil {
			return err
		}
		before := len(operator.responded)
		for _, checkpoint := range checkpoints {
			if err := operator.resolveApproval(ctx, checkpoint); err != nil {
				return err
			}
		}
		if err := operator.answerQuestion(ctx, feedback); err != nil {
			return err
		}
		if len(operator.responded) != before {
			return progress(*result)
		}
		return nil
	}
}

func (o *scriptedOperator) resolveApproval(ctx context.Context, checkpoint wire.CheckpointEvent) error {
	if checkpoint.Status != wire.CheckpointStatusPending || o.responded[checkpoint.ID] {
		return nil
	}
	if checkpoint.Kind != wire.CheckpointKindToolApproval {
		return interactionExhausted{}
	}
	// Only the coordinator's own session can carry the prepared call; a child
	// reusing that call id is candidate work.
	option, preparedDenial := "", false
	if checkpoint.SessionID == "" || checkpoint.SessionID == o.result.SessionID {
		option = fixtureApprovalOption(o.result.Sandbox, checkpoint)
		preparedDenial = fixtureDenial(o.result.Sandbox, checkpoint)
	}
	responseKind := "approval_rejected"
	counted := option == "" && !preparedDenial
	if preparedDenial {
		responseKind = "fixture_access_denied"
	}
	if option != "" {
		responseKind = "fixture_approved_once"
	}
	if counted && checkpoint.ToolApproval != nil && checkpoint.ToolApproval.JoinedCount <= 1 {
		subject := string(checkpoint.ToolApproval.Plan.Subject.Kind)
		if rule, ok := o.rule(subject); ok {
			if rule.Decision == "approve" {
				if option = ruleOption(rule, checkpoint.ToolApproval.Plan); option != "" {
					responseKind, counted = "fixture_rule_approved", false
				}
			} else {
				// The first declared denial is the fixture's boundary; asking again spends the allowance.
				responseKind, counted = "fixture_rule_denied", o.ruleDenials[subject] > 0
				o.ruleDenials[subject]++
			}
		}
	}
	if counted && o.interventions >= o.policy.MaxInterventions {
		return interactionExhausted{}
	}
	sessionID := checkpoint.SessionID
	if sessionID == "" {
		sessionID = o.result.SessionID
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/checkpoints/" + url.PathEscape(checkpoint.ID)
	request := wire.ResolveCheckpointRequest{Kind: checkpoint.Kind, Action: wire.ApprovalActionReject, Guidance: o.policy.ApprovalGuidance}
	if option != "" {
		request.Action, request.OptionID, request.Guidance = wire.ApprovalActionApprove, option, ""
	}
	if err := o.client.scriptedResponse(ctx, path, request); err != nil {
		return err
	}
	if counted {
		o.interventions++
	}
	o.record(responseKind, checkpoint.ID, sessionID)
	return nil
}

func (o *scriptedOperator) rule(subject string) (ApprovalRule, bool) {
	for _, rule := range o.rules {
		if rule.Subject == subject {
			return rule, true
		}
	}
	return ApprovalRule{}, false
}

// ruleOption selects the host-authored option the rule's rung names; a plan
// without that rung offers nothing the fixture declared.
func ruleOption(rule ApprovalRule, plan wire.ApprovalPlan) string {
	for _, option := range plan.Options {
		if option.Disabled || option.DecisionAction != wire.ApprovalOptionDecisionApprove {
			continue
		}
		switch rule.Rung {
		case approvalRungOnce:
			if option.Kind == wire.ApprovalOptionKindCurrentAction && option.Rung == wire.ApprovalOptionRungOnce {
				return option.ID
			}
		case approvalRungTask:
			if option.Kind == wire.ApprovalOptionKindLease && option.Scope == wire.ApprovalGrantScopeChat && option.Rung == wire.ApprovalOptionRungChat {
				return option.ID
			}
		}
	}
	return ""
}

func (o *scriptedOperator) answerQuestion(ctx context.Context, feedback *FeedbackRequest) error {
	if feedback == nil {
		return nil
	}
	key := fmt.Sprintf("%s/%s/%d", feedback.RunID, feedback.PhaseID, feedback.IssuedRevision)
	if o.responded[key] {
		return nil
	}
	if feedback.Secret != nil {
		return interactionExhausted{}
	}
	choice := feedback.ResponseType == string(wire.FeedbackResponseSingleChoice) || feedback.ResponseType == string(wire.FeedbackResponseMultiChoice)
	if !choice && feedback.ResponseType != "" && feedback.ResponseType != string(wire.FeedbackResponseText) {
		return interactionExhausted{}
	}
	if o.interventions >= o.policy.MaxInterventions {
		return interactionExhausted{}
	}
	if choice {
		if len(feedback.Options) == 0 {
			return interactionExhausted{}
		}
		// Unregistered choices use the first option and spend one intervention.
		if err := o.decide(ctx, feedback, feedback.Options[0], o.policy.Answer); err != nil {
			return err
		}
		o.interventions++
		o.record("unregistered_ask_answered", key, o.result.SessionID)
		return nil
	}
	path := "/v1/workflow-runs/" + url.PathEscape(feedback.RunID) + "/feedback/" + url.PathEscape(feedback.PhaseID)
	request := wire.ResolveUserFeedbackRequest{ExpectedRevision: feedback.IssuedRevision, Response: o.policy.Answer}
	if err := o.client.scriptedResponse(ctx, path, request); err != nil {
		return err
	}
	o.interventions++
	o.record("task_restated", key, o.result.SessionID)
	return nil
}

func (o *scriptedOperator) decide(ctx context.Context, feedback *FeedbackRequest, choice, comment string) error {
	path := "/v1/workflow-runs/" + url.PathEscape(feedback.RunID) + "/decisions/" + url.PathEscape(feedback.PhaseID)
	return o.client.scriptedResponse(ctx, path, wire.ResolveWorkflowDecisionRequest{ExpectedRevision: feedback.IssuedRevision, Choice: choice, Comment: comment})
}

func (o *scriptedOperator) record(kind, id, sessionID string) {
	o.responded[id] = true
	o.result.AutomaticResponses = append(o.result.AutomaticResponses, AutomaticResponse{Kind: kind, ID: id, SessionID: sessionID})
}

func (c *liveClient) scriptedResponse(ctx context.Context, path string, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unattended response: HTTP %d", resp.StatusCode)
	}
	return nil
}
