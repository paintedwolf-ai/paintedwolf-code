package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDocumentAdmissionRequiresWritableSource(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new document"
		if existing {
			name = "retained document"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "saved"})
			if existing {
				_, err := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "first-window", nil)
				testutil.FailErr(t, "open writable document", err)
			}
			path := filepath.Join(f.root, "a.txt")
			testutil.FailErr(t, "make source read-only", os.Chmod(path, 0o444))
			_, err := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "second-window", nil)
			if !errors.Is(err, ErrReadOnly) {
				t.Fatalf("open read-only source: %v", err)
			}
			testutil.FailErr(t, "make source writable", os.Chmod(path, 0o644))
			_, err = f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "second-window", nil)
			testutil.FailErr(t, "open writable source", err)
		})
	}
}
