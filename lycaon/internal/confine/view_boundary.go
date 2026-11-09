package confine

import (
	"slices"
	"strconv"
	"strings"
)

// ViewRootBoundary binds one mounted workspace root to its authoritative host
// source directory and the effective policy-file inventory of that root.
type ViewRootBoundary struct {
	RootID          string
	WorkspacePath   string
	SourcePath      string
	PolicyFiles     []string
	PolicyInventory bool
}

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
	// Roots binds each mounted workspace root to its source tree and effective policy-file inventory.
	Roots []ViewRootBoundary
}

func (v ViewBoundary) normalized() ViewBoundary {
	return ViewBoundary{
		DeniedPaths: normalizePathList(v.DeniedPaths), ReopenedPaths: normalizePathList(v.ReopenedPaths), ReopenedReadPaths: normalizePathList(v.ReopenedReadPaths),
		WriteDeniedPaths: normalizePathList(v.WriteDeniedPaths), DeniedLoopbackPorts: normalizeListenPorts(v.DeniedLoopbackPorts),
		Roots: normalizeViewRoots(v.Roots),
	}
}

// addViewStorageFloor closes the view's storage last, so no earlier allow-back reopens it. The paths
// are canonical, because a rule on an alias of a path does not apply to the path itself.
func addViewStorageFloor(rules *FilesystemRules, view ViewBoundary) {
	denied := resolveEach(slices.Clone(view.DeniedPaths))
	reopened := resolveEach(slices.Clone(view.ReopenedPaths))
	reopenedRead := resolveEach(slices.Clone(view.ReopenedReadPaths))
	writeDenied := resolveEach(slices.Clone(view.WriteDeniedPaths))
	rules.view = ViewBoundary{
		DeniedPaths:         denied,
		ReopenedPaths:       reopened,
		ReopenedReadPaths:   reopenedRead,
		WriteDeniedPaths:    writeDenied,
		DeniedLoopbackPorts: view.DeniedLoopbackPorts,
		Roots:               resolveViewRoots(view.Roots),
	}
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	rules.add(fsRule{op: opWrite, matches: subpathMatches(denied, FloorControlPlane)})
	readable := append(slices.Clone(reopenedRead), reopened...)
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(readable, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(traversalAncestors(denied, readable), "")})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches(reopened, "")})
	rules.add(fsRule{op: opWrite, matches: subpathMatches(writeDenied, FloorControlPlane)})
}

func normalizeViewRoots(roots []ViewRootBoundary) []ViewRootBoundary {
	if len(roots) == 0 {
		return nil
	}
	out := make([]ViewRootBoundary, 0, len(roots))
	for _, r := range roots {
		wp := normalizeSinglePath(r.WorkspacePath)
		sp := normalizeSinglePath(r.SourcePath)
		if wp == "" && sp == "" && strings.TrimSpace(r.RootID) == "" && len(r.PolicyFiles) == 0 && !r.PolicyInventory {
			continue
		}
		out = append(out, ViewRootBoundary{
			RootID:          strings.TrimSpace(r.RootID),
			WorkspacePath:   wp,
			SourcePath:      sp,
			PolicyFiles:     normalizePolicyFiles(r.PolicyFiles),
			PolicyInventory: r.PolicyInventory,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveViewRoots(roots []ViewRootBoundary) []ViewRootBoundary {
	if len(roots) == 0 {
		return nil
	}
	out := make([]ViewRootBoundary, 0, len(roots))
	for _, r := range roots {
		out = append(out, ViewRootBoundary{
			RootID:          strings.TrimSpace(r.RootID),
			WorkspacePath:   resolveSinglePath(r.WorkspacePath),
			SourcePath:      resolveSinglePath(r.SourcePath),
			PolicyFiles:     normalizePolicyFiles(r.PolicyFiles),
			PolicyInventory: r.PolicyInventory,
		})
	}
	return out
}

func normalizeSinglePath(p string) string {
	if list := normalizePathList([]string{p}); len(list) > 0 {
		return list[0]
	}
	return ""
}

func resolveSinglePath(p string) string {
	if list := resolveEach([]string{p}); len(list) > 0 {
		return list[0]
	}
	return ""
}

func normalizePolicyFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(f), "\\", "/"), "./")
		if f != "" && f != "." && f != "/" {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
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
