package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// wireExceptionalCapability installs AF_UNIX / direct-IP runtimes, ledger hooks,
// and the ExternalAccess builder used by tool results and protection chrome.
func (b sessionWiring) wireExceptionalCapability() error {
	socketCapabilityRT := approvalstate.NewSocketCapabilityRuntime()
	b.socketCapabilityRT = socketCapabilityRT
	b.toolRuntime.Executor.SetSocketCapabilityRuntime(socketCapabilityAdapter{rt: socketCapabilityRT})
	if b.settingsSvc != nil && b.settingsSvc.Approvals != nil {
		b.toolRuntime.Executor.SetDurableSocketSource(b.settingsSvc.Approvals.SocketPathsForProject)
	}
	b.toolRuntime.Executor.SetApprovalsDisabled(b.toolRuntime.ApprovalsDisabled)
	if err := b.mgr.RegisterSessionCleanup("socket-capabilities", 51, func(_ context.Context, sessionID string) error {
		socketCapabilityRT.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if err := b.mgr.RegisterSessionDisposal("socket-capability-grants", 51, func(_ context.Context, sessionID string) error {
		socketCapabilityRT.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}

	directIPCapabilityRT := approvalstate.NewDirectIPCapabilityRuntime()
	b.directIPCapabilityRT = directIPCapabilityRT
	b.toolRuntime.Executor.SetDirectIPCapabilityRuntime(directIPCapabilityAdapter{rt: directIPCapabilityRT})
	if err := b.mgr.RegisterSessionCleanup("direct-ip-capabilities", 52, func(_ context.Context, sessionID string) error {
		directIPCapabilityRT.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if err := b.mgr.RegisterSessionDisposal("direct-ip-grants", 52, func(_ context.Context, sessionID string) error {
		directIPCapabilityRT.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if b.authzCapturer != nil {
		rec := b.authzCapturer.Recorder
		b.toolRuntime.Executor.SetDirectIPLifecycleHook(func(ev tools.DirectIPLifecycleEvent) {
			rec.AppendDirectIPLifecycle(context.Background(), authzledger.DirectIPLifecycleRecord{
				SessionID:            ev.SessionID,
				Phase:                string(ev.Phase),
				Tool:                 "command",
				AuthorizationSource:  ev.AuthorizationSource,
				DeclaredDestinations: append([]string(nil), ev.DeclaredDestinations...),
				Background:           ev.Background,
			})
		})
		b.mgr.SetDirectIPReconstructHook(func(sessionID, _, _ string) {
			rec.AppendDirectIPLifecycle(context.Background(), authzledger.DirectIPLifecycleRecord{
				SessionID:  sessionID,
				Phase:      string(tools.DirectIPLifecycleReconstructed),
				Tool:       "command",
				Background: true,
				// Recovery carries no declared destinations.
			})
		})
	}

	tools.SetExternalAccessBuilder(func(in tools.ExternalAccessBuildInput) *api.ExternalAccess {
		endpoints := make([]authzcontext.ExternalAccessEndpointInput, 0, len(in.Endpoints))
		for _, ep := range in.Endpoints {
			endpoints = append(endpoints, authzcontext.ExternalAccessEndpointInput{
				Host: ep.Host, Port: ep.Port, Transport: ep.Transport, Allowed: ep.Allowed, Attempts: ep.Attempts,
			})
		}
		sockets := make([]authzcontext.ExternalAccessSocketInput, 0, len(in.Sockets))
		for _, s := range in.Sockets {
			sockets = append(sockets, authzcontext.ExternalAccessSocketInput{
				ApprovedPath: s.ApprovedPath, ResolvedPath: s.ResolvedPath, Scope: s.Scope,
			})
		}
		return authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
			Endpoints:            endpoints,
			Sockets:              sockets,
			DeclaredDestinations: in.DeclaredDestinations,
			Direct:               in.Direct,
			FullBypass:           in.FullBypass,
		})
	})
	return nil
}
