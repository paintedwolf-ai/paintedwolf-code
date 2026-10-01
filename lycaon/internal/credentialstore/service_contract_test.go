package credentialstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestShippedEngineEnablesReleaseCredentialBuild(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "stage-engine.sh"))
	testutil.FailErr(t, "read stage-engine.sh", err)
	if !strings.Contains(string(raw), `BUILD_ARGS+=(-tags=paintedwolf_release)`) {
		t.Fatal("shipped engine build does not enable the release credential provider")
	}
}
