package historyretention

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadPolicyPreservesIOErrors(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create unreadable policy entry", os.Mkdir(filepath.Join(root, PolicyFilename), 0o700))
	_, err := readPolicy(root)
	var pathError *fs.PathError
	if !errors.As(err, &pathError) || errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("policy read error = %v, want filesystem error", err)
	}
}
