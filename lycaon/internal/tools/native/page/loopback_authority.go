package page

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/url"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/tools"
)

// requireLoopbackAuthority checks that loopback connect is granted for URL-mode targets.
func requireLoopbackAuthority(rawURL string, tctx tools.ToolContext) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	port, ok := loopbackTargetPort(rawURL)
	if !ok {
		return nil
	}
	if tctx.LoopbackConnectGranted && portGranted(tctx.LoopbackConnectPorts, port) {
		return nil
	}
	return &toolrejection.ToolReject{
		Code: isolation.CodeTryLoopbackConnect,
		Data: map[string]any{"port": port, "url": rawURL},
	}
}

// loopbackTargetPort returns the local port a page target names. A target that is
// not local is not this axis's business — the capture path refuses it on its own.
func loopbackTargetPort(rawURL string) (uint16, bool) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Host == "" {
		return 0, false
	}
	if !egress.SyntacticLoopback(target.Hostname()) {
		return 0, false
	}
	port := uint16(80)
	if strings.EqualFold(target.Scheme, "https") {
		port = 443
	}
	if explicit := target.Port(); explicit != "" {
		parsed, err := strconv.ParseUint(explicit, 10, 16)
		if err != nil || parsed == 0 {
			return 0, false
		}
		port = uint16(parsed)
	}
	return port, true
}

// portGranted reports whether a held grant reaches port. An unnarrowed grant is
// every local port, as the reviewed task rung says.
func portGranted(held []uint16, port uint16) bool {
	if len(held) == 0 {
		return true
	}
	for _, candidate := range held {
		if candidate == port {
			return true
		}
	}
	return false
}
