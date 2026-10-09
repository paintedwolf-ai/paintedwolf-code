package security

import (
	"context"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type SessionLifetime interface {
	RegisterSessionCleanup(string, int, func(context.Context, string) error) error
	RegisterSessionDisposal(string, int, func(context.Context, string) error) error
}
type DirectIPRecorder interface {
	AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord)
}

func (b *Runtime) BuildExceptional(control *toolexecution.Capabilities, approvals *settings.ApprovalStore, disabled func(string) bool, lifetime SessionLifetime, recorder DirectIPRecorder, reconstruct func(session.DirectIPReconstructHook)) error {
	socketCapabilityRT := approvalstate.NewSocketCapabilityRuntime()
	b.Sockets = socketCapabilityRT
	control.SetSocketCapabilityRuntime(socketCapabilityRT)
	if approvals != nil {
		control.SetDurableSocketSource(approvals.SocketPathsForProject)
	}
	control.SetApprovalsDisabled(disabled)
	if err := lifetime.RegisterSessionCleanup("socket-capabilities", 51, func(_ context.Context, sessionID string) error {
		socketCapabilityRT.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if err := lifetime.RegisterSessionDisposal("socket-capability-grants", 51, func(_ context.Context, sessionID string) error {
		socketCapabilityRT.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}

	directIPCapabilityRT := approvalstate.NewDirectIPCapabilityRuntime()
	b.DirectIP = directIPCapabilityRT
	control.SetDirectIPCapabilityRuntime(directIPCapabilityRT)
	if err := lifetime.RegisterSessionCleanup("direct-ip-capabilities", 52, func(_ context.Context, sessionID string) error {
		directIPCapabilityRT.ReleaseRun(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if err := lifetime.RegisterSessionDisposal("direct-ip-grants", 52, func(_ context.Context, sessionID string) error {
		directIPCapabilityRT.ForgetSession(sessionID)
		return nil
	}); err != nil {
		return err
	}
	if recorder != nil {
		rec := recorder
		control.SetDirectIPLifecycleHook(func(ev tools.DirectIPLifecycleEvent) {
			rec.AppendDirectIPLifecycle(context.Background(), authzledger.DirectIPLifecycleRecord{
				SessionID:            ev.SessionID,
				Phase:                string(ev.Phase),
				Tool:                 "command",
				AuthorizationSource:  ev.AuthorizationSource,
				DeclaredDestinations: append([]string(nil), ev.DeclaredDestinations...),
				Background:           ev.Background,
			})
		})
		reconstruct(func(sessionID, _, _ string) {
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
