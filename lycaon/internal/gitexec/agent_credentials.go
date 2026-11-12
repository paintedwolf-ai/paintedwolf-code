package gitexec

import (
	"context"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/gitcredential"
)

// agentCredentialStore is the host store an agent's credential service reads.
var agentCredentialStore = gitcredential.PlatformBackend

// TestingUseAgentCredentialStore makes agent credential services read store
// until the returned restore runs.
func TestingUseAgentCredentialStore(store gitcredential.Backend) (restore func()) {
	previous := agentCredentialStore
	agentCredentialStore = func() gitcredential.Backend { return store }
	return func() { agentCredentialStore = previous }
}

// AgentCredentials is the credential service one agent network command's Git
// asks. It answers get only, only for recipients admitted to it, from the
// host's own store; the confined helper never reads the store itself.
type AgentCredentials struct {
	service *gitcredential.Service
	scope   *gitcredential.Scope
}

// StartAgentCredentials starts a service with an empty scope; Admit adds each
// recipient once its contact is authorized.
func StartAgentCredentials(ctx context.Context) (*AgentCredentials, error) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, err
	}
	scope := &gitcredential.Scope{}
	service, err := gitcredential.Serve(ctx, gitcredential.ServeOptions{GitBinary: bin, Store: agentCredentialStore(), Scope: scope})
	if err != nil {
		return nil, err
	}
	return &AgentCredentials{service: service, scope: scope}, nil
}

// Socket is the service's socket, which the agent's confinement must reach.
func (c *AgentCredentials) Socket() string { return c.service.Path() }

// Admit scopes credentials to the HTTP recipients among urls. Other transports
// authenticate through the confinement's own SSH path.
func (c *AgentCredentials) Admit(urls ...string) {
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		c.scope.Admit(gitcredential.Recipient{Protocol: u.Scheme, Host: strings.ToLower(u.Host)})
	}
}

// Close stops the service.
func (c *AgentCredentials) Close() error { return c.service.Close() }

// HostSSHAgentSocket is the user's SSH agent, which an agent network command
// may reach only through an explicit socket grant on its confined launch.
func HostSSHAgentSocket(ctx context.Context) string { return agentSocketResolver(ctx) }
