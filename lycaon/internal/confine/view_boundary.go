package confine

import (
	"slices"
	"strconv"
	"strings"
)

// ViewBoundary names what the processes of a mounted worker view never reach directly. A view's
// bytes belong to the server that serves its mount: a process that reached them would bypass
// capture, and one that reached the listener could speak to the server itself.
type ViewBoundary struct {
	// DeniedPaths are refused for reading and writing, after every grant.
	DeniedPaths []string
	// DeniedLoopbackPorts are the live view listeners, refused after every loopback grant. Only a
	// process is refused: the kernel's own client traffic is not subject to its profile.
	DeniedLoopbackPorts []uint16
}

func (v ViewBoundary) normalized() ViewBoundary {
	return ViewBoundary{DeniedPaths: normalizePathList(v.DeniedPaths), DeniedLoopbackPorts: normalizeListenPorts(v.DeniedLoopbackPorts)}
}

// addViewStorageFloor closes the view's storage last, so no earlier allow-back reopens it. The paths
// are canonical, because a rule on an alias of a path does not apply to the path itself.
func addViewStorageFloor(rules *FilesystemRules, view ViewBoundary) {
	denied := resolveEach(slices.Clone(view.DeniedPaths))
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	rules.add(fsRule{op: opWrite, matches: subpathMatches(denied, FloorControlPlane)})
}

// writeViewListenerDenials follows every network allow, since the last matching rule decides.
func writeViewListenerDenials(b *strings.Builder, view ViewBoundary, deny string) {
	for _, port := range view.DeniedLoopbackPorts {
		b.WriteString("(deny network-outbound (remote ip " + sbplString("localhost:"+strconv.Itoa(int(port))) + ")" + deny + ")\n")
	}
}
