package gitexec

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// ErrLaunchAuthorityRequired refuses a Git launch inside an agent's tool call
// that names neither the agent's admitted launch nor a host purpose.
var ErrLaunchAuthorityRequired = errors.New("gitexec: agent context requires an admitted launch or a named host purpose")

type authorityKey struct{}

type authority struct {
	agentEntry bool
	hostReason string
	agent      *AgentLaunch
}

// AgentLaunch is one agent Git command's admitted launch.
type AgentLaunch struct {
	Plan exec.LaunchPlan
	// Credentials serves a network command's Git; nil leaves it without any.
	Credentials *AgentCredentials
}

// AgentEntry marks ctx as an agent's tool call. Git launched under it runs
// only with WithAgentLaunch or WithHostPurpose; omission refuses rather than
// inheriting host authority.
func AgentEntry(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorityKey{}, authority{agentEntry: true})
}

// WithAgentLaunch runs every Git process under ctx with the agent's admitted
// confined launch.
func WithAgentLaunch(ctx context.Context, launch AgentLaunch) context.Context {
	return context.WithValue(ctx, authorityKey{}, authority{agentEntry: true, agent: &launch})
}

// WithHostPurpose runs Git under ctx as host bookkeeping the agent did not
// choose. Inside an agent tool call each purpose is a reviewed exception.
func WithHostPurpose(ctx context.Context, purpose string) context.Context {
	current, _ := ctx.Value(authorityKey{}).(authority)
	return context.WithValue(ctx, authorityKey{}, authority{agentEntry: current.agentEntry, hostReason: strings.TrimSpace(purpose)})
}

// AgentLaunchFrom reports the admitted agent launch on ctx.
func AgentLaunchFrom(ctx context.Context) (AgentLaunch, bool) {
	a, _ := ctx.Value(authorityKey{}).(authority)
	if a.agent == nil {
		return AgentLaunch{}, false
	}
	return *a.agent, true
}

// launchPlan selects the process authority for one Git subprocess.
func launchPlan(ctx context.Context, subject string) (exec.LaunchPlan, error) {
	a, _ := ctx.Value(authorityKey{}).(authority)
	switch {
	case a.agent != nil:
		plan := a.agent.Plan
		plan.Subject = subject
		return plan, nil
	case a.hostReason != "":
		return exec.HostLaunch(subject + ":" + a.hostReason), nil
	case a.agentEntry:
		return exec.LaunchPlan{}, ErrLaunchAuthorityRequired
	default:
		return exec.HostLaunch(subject), nil
	}
}

func agentLaunched(ctx context.Context) bool {
	_, ok := AgentLaunchFrom(ctx)
	return ok
}

// hostPurposeInAgentEntry reports host bookkeeping inside an agent's tool
// call; every such purpose is local.
func hostPurposeInAgentEntry(ctx context.Context) bool {
	a, _ := ctx.Value(authorityKey{}).(authority)
	return a.agent == nil && a.agentEntry && a.hostReason != ""
}
