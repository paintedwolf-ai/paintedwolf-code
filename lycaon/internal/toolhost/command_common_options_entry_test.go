//go:build darwin || linux

package toolhost

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/testutil"
)

// commonOptionGoldens are the catalog golden programs for the tar, diff,
// date, realpath and basename option forms.
var commonOptionGoldens = []string{"diff-new-file-tree", "diff-strip-trailing-cr", "date-relative-day", "date-bsd-parse", "realpath-relative-to", "basename-suffix-multiple", "tar-exclude-vcs"}

func TestCommandCommonOptionGoldenProgramsRunThroughTheEntry(t *testing.T) {
	runEntryGoldens(t, commonOptionGoldens)
}

func xzTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var plain, out bytes.Buffer
	tw := tar.NewWriter(&plain)
	for _, name := range []string{"release/", "release/bin/tool", "../escape"} {
		body, ok := entries[name]
		if !ok {
			continue
		}
		kind := byte(tar.TypeReg)
		if name[len(name)-1] == '/' {
			kind = tar.TypeDir
		}
		testutil.FailErr(t, "tar header", tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: kind}))
		_, err := tw.Write([]byte(body))
		testutil.FailErr(t, "tar body", err)
	}
	testutil.FailErr(t, "tar close", tw.Close())
	w, err := xz.NewWriter(&out)
	testutil.FailErr(t, "xz writer", err)
	_, err = w.Write(plain.Bytes())
	testutil.FailErr(t, "xz write", err)
	testutil.FailErr(t, "xz close", w.Close())
	return out.Bytes()
}

// TestCommandTarExtractsXZIntoScratchWithItsGuards: tar -xJf extracts a
// .tar.xz into session scratch, and a traversal member refuses with nothing
// written outside the destination.
func TestCommandTarExtractsXZIntoScratchWithItsGuards(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	testutil.FailErr(t, "write release", os.WriteFile(filepath.Join(env.Dir, "release.tar.xz"), xzTar(t, map[string]string{"release/": "", "release/bin/tool": "#!/bin/sh\n"}), 0o644))
	testutil.FailErr(t, "write crafted", os.WriteFile(filepath.Join(env.Dir, "crafted.tar.xz"), xzTar(t, map[string]string{"../escape": "x"}), 0o644))
	out, err := env.Call(t, t.Context(), `mkdir -p @scratch/x && tar -xJf release.tar.xz -C @scratch/x && cat @scratch/x/release/bin/tool`)
	testutil.FailErr(t, "extract release", err)
	if result := decodePipeOutcome(t, out); result.Refusal != nil || result.Status != 0 || result.Stdout != "#!/bin/sh\n" {
		t.Fatalf("tar -xJf %+v", result)
	}
	out, err = env.Call(t, t.Context(), `mkdir -p @scratch/y && tar -xJf crafted.tar.xz -C @scratch/y`)
	testutil.FailErr(t, "extract crafted", err)
	result := decodePipeOutcome(t, out)
	if result.Refusal == nil || result.Refusal.Code != "EXTRACT_ZIP_SLIP" {
		t.Fatalf("traversal member %+v", result)
	}
	if _, err := os.Stat(filepath.Join(env.scratchDir, "escape")); !os.IsNotExist(err) {
		t.Fatalf("traversal member was written: %v", err)
	}
}
