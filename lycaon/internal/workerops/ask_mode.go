package workerops

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// CheckAskInstruction projects the same target check used under the worker lock.
func (s *Service) CheckAskInstruction(ctx context.Context, sessionID string, args map[string]any) error {
	name, _ := args["worker"].(string)
	agent, _ := args["agent_type"].(string)
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	worker, err := s.Reader.Records.Resolve(ctx, actor, strings.TrimSpace(name))
	if err != nil {
		return err
	}
	return s.checkAskInstruction(worker, strings.TrimSpace(agent))
}

func (s *Service) checkAskInstruction(worker workerrecords.Worker, agent string) error {
	scope := api.TaskScope{Mode: api.TaskScopeMode(worker.ScopeMode)}
	writes, known := s.Agents(worker.AgentType)
	if err := workeradmission.RequireAskReadScope("instruct_worker", writes, known, scope); err != nil {
		return err
	}
	if agent == "" || agent == worker.AgentType {
		return nil
	}
	writes, known = s.Agents(agent)
	return workeradmission.RequireAskReadScope("instruct_worker", writes, known, scope)
}
