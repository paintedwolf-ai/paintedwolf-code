package confine

import (
	"log/slog"
	"os"
	"strings"
)

// NetworkLabel is a stable wire/log vocabulary for NetworkMode.
func NetworkLabel(m NetworkMode) string {
	switch m {
	case NetworkDirectIP:
		return "direct_ip"
	case NetworkProxyOnly:
		return "proxy_only"
	case NetworkDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// LogApplied records an applied process boundary.
func LogApplied(tool, sessionID string, c *Confinement) {
	if c == nil {
		return
	}
	slog.Info("process boundary selected",
		"host_execution", c.HostExecution,
		"process_control", c.ProcessControl,
		"component", "sandbox",
		"tool", tool,
		"session_id", sessionID,
		"network", NetworkLabel(c.Network),
		"socks_proxy_env", c.SocksProxyEnv && c.SocksProxyAddr != "",
		"browser_profile", c.Browser,
		"root_count", len(c.Roots),
		// Root values preserve denial provenance.
		"roots", strings.Join(c.Roots, string(os.PathListSeparator)),
		"socket_count", len(c.SocketGrants),
		"protected_write_count", len(c.ProtectedWriteGrants),
		"protected_read_count", len(c.ProtectedReadGrants),
	)
	logDroppedGrants(tool, sessionID, c)
}

// logDroppedGrants records approved authority that failed validation.
func logDroppedGrants(tool, sessionID string, c *Confinement) {
	for _, drop := range append(append([]ProtectedPathDrop(nil), c.DroppedProtectedWriteGrants...), c.DroppedProtectedReadGrants...) {
		slog.Warn("sandbox protected path grant dropped",
			"component", "sandbox",
			"tool", tool,
			"session_id", sessionID,
			"path", drop.Grant.ApprovedPath,
			"reason", string(drop.Reason),
		)
	}
	for _, drop := range c.DroppedSocketGrants {
		slog.Warn("sandbox socket grant dropped",
			"component", "sandbox",
			"tool", tool,
			"session_id", sessionID,
			"path", drop.Grant.ApprovedPath,
			"reason", string(drop.Reason),
		)
	}
}

func logConfinementPrepareError(err error) {
	slog.Warn("sandbox confinement request rejected",
		"component", "sandbox",
		"reason", "prepare_failed",
		"err", err.Error(),
	)
}

func logBrokerStartFailure(reason, diagnostic string) {
	slog.Warn("sandbox network mediation unavailable",
		"component", "sandbox",
		"reason", reason,
		"err", diagnostic,
	)
}

func logRefusal(tool, sessionID string, obs Observation) {
	slog.Warn("sandbox boundary refused",
		"component", "sandbox",
		"tool", tool,
		"session_id", sessionID,
		"network", obs.Network,
		"destination", obs.Destination,
		"signals", obs.Signals,
	)
}

func logSandboxRefusals(tool, sessionID string, refusals SandboxRefusals) {
	first := refusals.Refusals[0]
	slog.Debug("sandbox refused operations",
		"component", "sandbox",
		"tool", tool,
		"session_id", sessionID,
		"refusals", len(refusals.Refusals),
		"omitted", refusals.Omitted,
		"witness", string(refusals.Witness),
		"first_operation", first.Operation,
		"first_recovery", string(first.Recovery),
	)
}
