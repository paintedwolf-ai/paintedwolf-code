package gitexec

import (
	"context"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitcredential"
)

// credentialFillTimeout bounds one host-side lookup in the person's helpers.
const credentialFillTimeout = 30 * time.Second

// agentCredentialStore is what an agent's credential service reads.
var agentCredentialStore = func() gitcredential.Backend { return userHelpers{} }

// TestingUseAgentCredentialStore makes agent credential services read store
// until the returned restore runs.
func TestingUseAgentCredentialStore(store gitcredential.Backend) (restore func()) {
	previous := agentCredentialStore
	agentCredentialStore = func() gitcredential.Backend { return store }
	return func() { agentCredentialStore = previous }
}

// userHelpers answers from the person's own configured credential helpers,
// run by the host as their own Git would run them: the Keychain or vault the
// host translates, gh, a credential manager or a keyring helper. A helper may
// not prompt; nothing found is an empty answer.
type userHelpers struct{}

func (userHelpers) Get(c gitcredential.Credential) (gitcredential.Credential, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), credentialFillTimeout)
	defer cancel()
	remote := (&url.URL{Scheme: c.Protocol, Host: c.Host, Path: "/" + c.Path}).String()
	out, code, err := Run(ctx, os.TempDir(), []string{"credential", "fill"}, Opts{
		Profile: ProfileNetwork, RemoteURL: remote, Input: gitcredential.Request(c),
		Timeout: credentialFillTimeout, ExtraConfig: []string{"credential.interactive=never"},
	})
	if err != nil || code != 0 {
		return gitcredential.Credential{}, false, nil
	}
	found, err := gitcredential.Parse(out)
	if err != nil || found.Password == "" {
		return gitcredential.Credential{}, false, nil
	}
	return found, true, nil
}

func (userHelpers) Store(gitcredential.Credential) error { return nil }

func (userHelpers) Erase(gitcredential.Credential) error { return nil }

// AgentCredentials is the credential service one agent network command's Git
// asks. It answers get only, only for recipients admitted to it, and only with
// a found credential its release let go to that recipient; the confined helper
// never reads the person's helpers itself.
type AgentCredentials struct {
	service *gitcredential.Service
	scope   *gitcredential.Scope
}

// Recipient and Credential restate the scoped service's release vocabulary:
// agent code reaches saved credentials only through this door, so its release
// callback names the types here rather than the credential store's package.
type (
	Recipient  = gitcredential.Recipient
	Credential = gitcredential.Credential
)

// StartAgentCredentials starts a service with an empty scope; Admit adds each
// recipient once its contact is authorized, and release reviews each found
// credential before Git receives it.
func StartAgentCredentials(ctx context.Context, release gitcredential.ReleaseFunc) (*AgentCredentials, error) {
	bin, err := binaryPathFn()
	if err != nil {
		return nil, err
	}
	scope := &gitcredential.Scope{Release: release}
	service, err := gitcredential.Serve(ctx, gitcredential.ServeOptions{GitBinary: bin, Store: agentCredentialStore(), Scope: scope})
	if err != nil {
		return nil, err
	}
	return &AgentCredentials{service: service, scope: scope}, nil
}

// Socket is the service's socket, which the agent's confinement must reach.
func (c *AgentCredentials) Socket() string { return c.service.Path() }

// Admit scopes credentials to the HTTP recipients among urls, each with the
// user and repository path its URL names. Other transports authenticate
// through the confinement's own SSH path.
func (c *AgentCredentials) Admit(urls ...string) {
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		c.scope.Admit(gitcredential.Recipient{
			Protocol: u.Scheme, Host: strings.ToLower(u.Host),
			Username: u.User.Username(), Path: strings.TrimPrefix(u.Path, "/"),
		})
	}
}

// Declined is each admitted recipient whose found credential the release did
// not let go, as a URL.
func (c *AgentCredentials) Declined() []string {
	var out []string
	for _, r := range c.scope.Declined() {
		out = append(out, (&url.URL{Scheme: r.Protocol, Host: r.Host}).String())
	}
	return out
}

// Close stops the service.
func (c *AgentCredentials) Close() error { return c.service.Close() }

// HostSSHAgentSocket is the user's SSH agent, which an agent network command
// may reach only through an explicit socket grant on its confined launch.
func HostSSHAgentSocket(ctx context.Context) string { return agentSocketResolver(ctx) }
