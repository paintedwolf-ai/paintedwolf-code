package confine

import (
	"slices"
	"strconv"
	"strings"
)

// ViewBoundary names what the processes of a worker's workspace never reach directly. A mounted
// view's bytes belong to the server that serves its mount: a process that reached them would bypass
// capture, and one that reached the listener could speak to the server itself.
type ViewBoundary struct {
	// DeniedPaths are refused for reading and writing, after every grant.
	DeniedPaths []string
	// ReopenedPaths are the worker's own roots, below a denied path.
	ReopenedPaths []string
	// ReopenedReadPaths stay read-only below a denied path, such as the object store a private
	// repository borrows from.
	ReopenedReadPaths []string
	// WriteDeniedPaths stay readable and are refused for writing, after everything above: a pristine
	// tree is the exact base of a later merge.
	WriteDeniedPaths []string
	// DeniedLoopbackPorts are the live view listeners. Only a process is refused: the kernel's own
	// client traffic is not subject to its profile.
	DeniedLoopbackPorts []uint16
}

func (v ViewBoundary) normalized() ViewBoundary {
	return ViewBoundary{
		DeniedPaths: normalizePathList(v.DeniedPaths), ReopenedPaths: normalizePathList(v.ReopenedPaths), ReopenedReadPaths: normalizePathList(v.ReopenedReadPaths),
		WriteDeniedPaths: normalizePathList(v.WriteDeniedPaths), DeniedLoopbackPorts: normalizeListenPorts(v.DeniedLoopbackPorts),
	}
}

// addViewStorageFloor closes the view's storage last, so no earlier allow-back reopens it. The paths
// are canonical, because a rule on an alias of a path does not apply to the path itself.
func addViewStorageFloor(rules *FilesystemRules, view ViewBoundary) {
	denied := resolveEach(slices.Clone(view.DeniedPaths))
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	rules.add(fsRule{op: opWrite, matches: subpathMatches(denied, FloorControlPlane)})
	reopened := resolveEach(slices.Clone(view.ReopenedPaths))
	readable := append(resolveEach(slices.Clone(view.ReopenedReadPaths)), reopened...)
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(readable, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(traversalAncestors(denied, readable), "")})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches(reopened, "")})
	rules.add(fsRule{op: opWrite, matches: subpathMatches(resolveEach(slices.Clone(view.WriteDeniedPaths)), FloorControlPlane)})
}

// withoutViewListeners keeps a narrowed loopback grant from ever naming a view's listener.
func withoutViewListeners(ports []uint16, view ViewBoundary) []uint16 {
	return slices.DeleteFunc(slices.Clone(ports), func(port uint16) bool {
		return slices.Contains(view.DeniedLoopbackPorts, port)
	})
}

// writeViewListenerDenials follows every network allow, since the last matching rule decides. Under a
// loopback grant of every port (or direct IP) this does not hold: on macOS a port-level deny let
// about half of connections through. Only the default deny refuses them reliably, and there the
// server's per-view capability is what keeps a connected process out.
func writeViewListenerDenials(b *strings.Builder, view ViewBoundary, deny string) {
	for _, port := range view.DeniedLoopbackPorts {
		b.WriteString("(deny network-outbound (remote ip " + sbplString("localhost:"+strconv.Itoa(int(port))) + ")" + deny + ")\n")
	}
}
