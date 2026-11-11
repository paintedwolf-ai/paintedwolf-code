// Command bwrap is a launchguard fixture shim, not the real bubblewrap.
//
// The protected desktop runs inside a Landlock file-system domain, and the
// kernel denies every mount(2) from a landlocked process. The runner image's
// gdk-pixbuf delegates SVG icons to glycin, whose loaders run under bwrap;
// inside the desktop bwrap always dies at its first mount ("Failed to make /
// slave"). glycin's availability probe only recognizes a fixed list of
// namespace-creation failures, so it misreads that death as "bwrap works" and
// the desktop aborts on the first themed icon load.
//
// This shim answers the probe in the one vocabulary glycin classifies as
// "sandboxing blocked", which makes glycin run its loader binary directly.
// That loader is already inside the desktop's execute closure, so icon
// loading succeeds without weakening any protection under test.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bwrap fixture shim: mount(2) is denied inside the protected desktop, so bubblewrap cannot set up its sandbox here")
	fmt.Fprintln(os.Stderr, "bwrap: No permissions to create a new namespace")
	os.Exit(1)
}
