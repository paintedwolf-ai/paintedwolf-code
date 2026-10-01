package tools

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/pkg/api"
)

// ExternalAccessBuildInput is the tools-layer input for stamping ToolResult.external_access.
// Built via SetExternalAccessBuilder to avoid an authzcontext import cycle.
type ExternalAccessBuildInput struct {
	Endpoints            []ExternalAccessEndpointFact
	Sockets              []ExternalAccessSocketFact
	DeclaredDestinations []string
	Direct               bool
	FullBypass           bool
}

// ExternalAccessEndpointFact is one mediated observation.
type ExternalAccessEndpointFact struct {
	Host      string
	Port      uint16
	Transport string
	Allowed   bool
	Attempts  int
}

// ExternalAccessSocketFact is one applied local-service socket.
type ExternalAccessSocketFact struct {
	ApprovedPath string
	ResolvedPath string
	Scope        string
}

var externalAccessBuilder func(ExternalAccessBuildInput) *api.ExternalAccess

// SetExternalAccessBuilder installs the authzcontext builder.
func SetExternalAccessBuilder(fn func(ExternalAccessBuildInput) *api.ExternalAccess) {
	externalAccessBuilder = fn
}

// CaptureExternalAccess builds ToolResult.external_access from applied machine
// facts on this invocation and stamps it on Out when present.
func CaptureExternalAccess(tctx ToolContext, hosts []confine.EgressHost, directApplied bool) {
	if tctx.Out == nil || externalAccessBuilder == nil {
		return
	}
	in := ExternalAccessBuildInput{
		Direct:     directApplied,
		FullBypass: confine.BypassEnabled() || confine.SandboxDisabled(),
	}
	if directApplied {
		in.DeclaredDestinations = append([]string(nil), tctx.DirectIPDeclared...)
	}
	for _, h := range hosts {
		in.Endpoints = append(in.Endpoints, ExternalAccessEndpointFact{
			Host:      h.Host,
			Port:      h.Port,
			Transport: h.Transport,
			Allowed:   h.Allowed,
			Attempts:  h.Attempts,
		})
	}
	for _, g := range tctx.SocketGrants {
		scope := "current_action"
		if tctx.SocketCapabilityRuntime != nil {
			for _, chat := range tctx.SocketCapabilityRuntime.AppliedGrants(tctx.ChatSessionID()) {
				if chat.ApprovedPath == g.ApprovedPath && chat.ResolvedPath == g.ResolvedPath {
					scope = "chat"
					break
				}
			}
		}
		in.Sockets = append(in.Sockets, ExternalAccessSocketFact{
			ApprovedPath: g.ApprovedPath,
			ResolvedPath: g.ResolvedPath,
			Scope:        scope,
		})
	}
	if len(in.Endpoints) == 0 && len(in.Sockets) == 0 && !in.Direct && !in.FullBypass {
		return
	}
	ea := externalAccessBuilder(in)
	if ea == nil {
		return
	}
	tctx.Out.ExternalAccess = ea
}

// ExternalAccessFromCapture returns a copy for ToolResult stamping.
func ExternalAccessFromCapture(ea *api.ExternalAccess) *api.ExternalAccess {
	if ea == nil {
		return nil
	}
	out := *ea
	if ea.Endpoints != nil {
		out.Endpoints = append([]api.ExternalAccessEndpoint(nil), ea.Endpoints...)
	}
	if ea.Sockets != nil {
		out.Sockets = append([]api.ExternalAccessSocket(nil), ea.Sockets...)
	}
	if ea.DeclaredDestinations != nil {
		out.DeclaredDestinations = append([]string(nil), ea.DeclaredDestinations...)
	}
	if ea.Detections != nil {
		out.Detections = append([]api.ExternalAccessDetection(nil), ea.Detections...)
	}
	if ea.Direct != nil {
		d := *ea.Direct
		out.Direct = &d
	}
	if ea.Modes != nil {
		out.Modes = append([]api.ExternalAccessMode(nil), ea.Modes...)
	}
	return &out
}
