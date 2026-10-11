package tools

import "github.com/lycaon/lycaon/internal/confine"

// LocalNetworkGrantOf reads the listener and loopback authority this invocation
// holds. Every process-spawning tool states it, granted or not: a refused bind
// reaches the caller only as the child's own errno.
func LocalNetworkGrantOf(tctx ToolContext) confine.LocalNetworkGrant {
	return confine.LocalNetworkGrant{
		Listen:        tctx.Local.LocalListenGranted,
		ListenPorts:   append([]uint16(nil), tctx.Local.LocalListenPorts...),
		Loopback:      tctx.Local.LoopbackConnectGranted,
		LoopbackPorts: append([]uint16(nil), tctx.Local.LoopbackConnectPorts...),
	}
}
