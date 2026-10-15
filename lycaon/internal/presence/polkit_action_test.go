package presence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

// The shell asks polkit for the action named for the bundle identifier, so
// the packaged policy file must declare exactly that action.
func TestPackagedPolkitActionIsNamedForTheBundle(t *testing.T) {
	path := filepath.Join("..", "..", "..", "packaging", "linux", "polkit", version.BundleID+".policy")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read polkit policy", err)
	if !strings.Contains(string(raw), `<action id="`+version.BundleID+`.confirm-presence">`) {
		t.Fatalf("%s does not declare %s.confirm-presence", path, version.BundleID)
	}
}
