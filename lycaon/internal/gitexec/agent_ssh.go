package gitexec

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/sshproxy"
)

// AgentSSH serves mediated SSH connections for one agent network Git invocation.
type AgentSSH struct {
	service     *sshproxy.Service
	hostService confine.HostService
	closeOnce   sync.Once
	closeErr    error
}

// StartAgentSSH allocates the per-invocation SSH service and admits its socket through confine.ServeHostSocket.
func StartAgentSSH(ctx context.Context, dial sshproxy.DialFunc) (*AgentSSH, error) {
	return productionExecution().startAgentSSH(ctx, dial)
}

func (e execution) startAgentSSH(ctx context.Context, dial sshproxy.DialFunc) (*AgentSSH, error) {
	bin, err := e.binaryPath()
	if err != nil {
		return nil, err
	}
	service, err := sshproxy.Serve(ctx, sshproxy.ServeOptions{GitBinary: bin, Dial: dial})
	if err != nil {
		return nil, err
	}
	hostService, release, err := confine.ServeHostSocket(service.Path())
	if err != nil {
		_ = service.Close()
		return nil, err
	}
	service.OnClose(release)
	return &AgentSSH{service: service, hostService: hostService}, nil
}

// Socket returns the private Unix socket path.
func (s *AgentSSH) Socket() string { return s.service.Path() }

// HostService returns the confinement admission for this service socket.
func (s *AgentSSH) HostService() confine.HostService { return s.hostService }

// HasDialer reports whether the SSH service has an outbound dialer bound.
func (s *AgentSSH) HasDialer() bool { return s.service.HasDialer() }

// BindExecution connects the service to the originating execution identity and
// live broker lease before any helper is served.
func (s *AgentSSH) BindExecution(executionID string, lease *confine.ActionLease) {
	s.service.SetExecutionID(executionID)
	s.service.SetDial(func(ctx context.Context, ep egressproxy.Endpoint) (sshproxy.DialResult, error) {
		res, err := lease.DialManagedTCP(ctx, ep)
		switch res.Disposition {
		case egressproxy.TCPDialEstablished:
			return sshproxy.DialResult{Conn: res.Conn, Reply: sshproxy.ReplySuccess, Stage: res.Stage, Outcome: sshproxy.OutcomeEstablished}, nil
		case egressproxy.TCPDialLeaseEnded:
			return sshproxy.DialResult{Reply: sshproxy.ReplyLeaseEnded, Stage: res.Stage, Outcome: sshproxy.OutcomeLeaseEnded}, err
		case egressproxy.TCPDialEndpointDenied:
			return sshproxy.DialResult{Reply: sshproxy.ReplyEndpointDenied, Stage: res.Stage, Outcome: sshproxy.OutcomeEndpointDenied, PolicyReason: res.PolicyReason}, err
		default:
			outcome := sshproxy.OutcomeDialFailed
			if res.Stage == "broker_setup" {
				outcome = sshproxy.OutcomeSetupFailed
			}
			return sshproxy.DialResult{Reply: sshproxy.ReplyDialFailed, Stage: res.Stage, Outcome: outcome, ErrorClass: res.ErrorClass}, err
		}
	})
}

// Observation returns the ordered verified connect attempts and summarized transport outcome for this launch.
func (s *AgentSSH) Observation(executionID, command string, exitCode int, failed bool, primaryDest, primaryHost string, primaryPort uint16) confine.GitTransportObservation {
	if s == nil {
		return confine.GitTransportObservation{}
	}
	raw := s.service.Attempts()
	attempts := make([]confine.GitTransportAttempt, len(raw))
	for i, a := range raw {
		attempts[i] = confine.GitTransportAttempt{
			Ordinal:         a.Ordinal,
			Destination:     a.Destination,
			Host:            a.Host,
			Port:            a.Port,
			Stage:           a.Stage,
			Outcome:         a.Outcome,
			ReplyCode:       uint8(a.Reply),
			PolicyReason:    a.PolicyReason,
			ErrorClass:      a.ErrorClass,
			SawServerBanner: a.SawServerBanner,
			ReachedUserAuth: a.ReachedUserAuth,
		}
	}
	setupStage, setupReason := "", ""
	if s.BindError() != nil && len(attempts) == 0 {
		setupStage = "helper_bind"
		setupReason = "helper_bind_failed"
	}
	return confine.SummarizeGitTransport(executionID, command, exitCode, failed, setupStage, setupReason, primaryDest, primaryHost, primaryPort, attempts)
}

// Bind records the started Git or supervisor PID once.
func (s *AgentSSH) Bind(pid int) error { return s.service.Bind(pid) }

// BindError returns any error recorded when the first Bind failed.
func (s *AgentSSH) BindError() error { return s.service.BindError() }

// Close closes active connections, wakes pending binds, releases admission and removes the service directory.
func (s *AgentSSH) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.service.Close() })
	return s.closeErr
}
