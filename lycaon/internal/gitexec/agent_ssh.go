package gitexec

import (
	"context"
	"net"
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

// BindLease connects the service to the live broker lease before any helper is served.
func (s *AgentSSH) BindLease(lease *confine.ActionLease) {
	s.service.SetDial(func(ctx context.Context, ep egressproxy.Endpoint) (net.Conn, sshproxy.ReplyCode, error) {
		conn, disp, err := lease.DialManagedTCP(ctx, ep)
		switch disp {
		case egressproxy.TCPDialEstablished:
			return conn, sshproxy.ReplySuccess, nil
		case egressproxy.TCPDialLeaseEnded:
			return nil, sshproxy.ReplyLeaseEnded, err
		case egressproxy.TCPDialEndpointDenied:
			return nil, sshproxy.ReplyEndpointDenied, err
		default:
			return nil, sshproxy.ReplyDialFailed, err
		}
	})
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
