//go:build linux

package packagegeneration

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLaunchCatalogErrorsPreserveTheirCauseAndVersion(t *testing.T) {
	for _, payload := range []string{"{", `{"format_version":42}`} {
		t.Run(payload, func(t *testing.T) {
			f := newInstallFixture(t)
			testutil.FailErr(t, "write launch catalog", os.WriteFile(filepath.Join(f.staging, LaunchCatalog), []byte(payload), 0o644))
			root, err := os.OpenRoot(f.staging)
			testutil.FailErr(t, "open staging root", err)
			defer func() { _ = root.Close() }()
			_, err = f.layout.systemAccess(root, "deb", "amd64")
			if err == nil {
				t.Fatal("invalid launch catalog was accepted")
			}
			if payload == "{" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("decode error lost its cause: %v", err)
			}
			if payload != "{" && !strings.Contains(err.Error(), "42") {
				t.Fatalf("unsupported format error lost its version: %v", err)
			}
		})
	}
}
