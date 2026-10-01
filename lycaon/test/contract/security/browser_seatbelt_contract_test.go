package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Managed browsing uses the confined launch path.
func TestBrowserLaunchUsesSeatbelt(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "browser", "session.go")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read session.go", err)
	text := string(data)
	for _, marker := range []string{
		"BrowserConfinement",
		"launchConfined",
		"execpkg.PrepareCommand",
		"LaunchManagedBrowser",
		// Fails closed like every other spawn seam: a confinement request that
		// errors must refuse, not launch an unconfined chrome while the posture
		// still reads as sandboxed.
		"RequireApplied",
		// Untrusted pages run on process plumbing, not the sidecar's credentials.
		"WithReducedEnvironment",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("%s missing %q", path, marker)
		}
	}
	confinePath := filepath.Join(root, "lycaon", "internal", "confine", "confine.go")
	cdata, err := os.ReadFile(confinePath)
	contractcheck.FailErr(t, "read confine.go", err)
	ctext := string(cdata)
	for _, marker := range []string{
		"func BrowserConfinement",
		"Browser bool",
		"NetworkDeny",
		"out.LoopbackConnect = true",
		"(allow hid-control)",
	} {
		if !strings.Contains(ctext, marker) {
			t.Fatalf("%s missing %q", confinePath, marker)
		}
	}
}
