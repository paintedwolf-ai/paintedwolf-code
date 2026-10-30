package history

import (
	"context"
	"reflect"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// RequestPolicies authenticates whole policy chains independently of their copy.
type RequestPolicies interface {
	PolicyMessageGroups([]api.Message) [][]api.Message
	PolicyContextNotice(context.Context, *api.Session) (api.Message, error)
}

func (m *Service) fitPolicyFrame(ctx context.Context, sess *api.Session, req modelcall.CompletionRequest, rows []api.Message, cfg compaction.CompactionConfig, budgets RequestBudgets, policies RequestPolicies, authenticated [][]api.Message, measure func(modelcall.CompletionRequest) modelcall.RequestBudget, reserve int) (modelcall.CompletionRequest, bool, error) {
	var groups [][]int
	for _, chain := range authenticated {
		var group []int
		for index, message := range req.Messages {
			for _, original := range chain {
				if original.ID == message.ID && reflect.DeepEqual(original, message) {
					group = append(group, index)
					break
				}
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 {
		return req, false, nil
	}
	notice, err := policies.PolicyContextNotice(ctx, sess)
	if err != nil {
		return req, false, err
	}
	minimal, err := m.fitRequestHistory(ctx, sess, rows, m.requestContinuation(ctx, sess, rows), cfg, 1, true)
	if err != nil {
		return req, false, err
	}
	deferred := make(map[int]bool)
	for _, group := range groups {
		for _, index := range group {
			deferred[index] = true
		}
	}
	selectMessages := func() []api.Message {
		messages := make([]api.Message, 0, len(req.Messages)+1)
		for index, message := range req.Messages {
			if !deferred[index] {
				messages = append(messages, message)
			}
		}
		return append(messages, notice)
	}
	fits := func() bool {
		candidate := req
		candidate.Messages = projectRequest(selectMessages(), rows).replace(minimal)
		budget := measure(candidate)
		return budget.Window <= 0 || budget.Input+reserve+budget.Headroom <= budget.Window
	}
	if !fits() {
		return req, false, nil
	}
	for _, group := range groups {
		var introduced []int
		for _, index := range group {
			if deferred[index] {
				introduced = append(introduced, index)
				delete(deferred, index)
			}
		}
		if !fits() {
			for _, index := range introduced {
				deferred[index] = true
			}
		}
	}
	if len(deferred) == 0 {
		return req, false, nil
	}
	req.Messages = selectMessages()
	fitted, err := m.FitRequest(ctx, sess, rows, req, budgets, nil)
	return fitted, true, err
}
