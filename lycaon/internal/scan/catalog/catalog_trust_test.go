package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateExternalArgvBareNameOK(t *testing.T) {
	if err := scancatalog.ValidateExternalArgv([]string{"semgrep", "--sarif"}, []string{t.TempDir()}); err != nil {
		testutil.FailErr(t, "ValidateExternalArgv bare", err)
	}
}

func TestValidateExternalArgvAbsOutsideOK(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write bin", err)
	}
	// Bare flags + {{project_dir}} token (expanded at run) are allowed in later argv.
	if err := scancatalog.ValidateExternalArgv([]string{bin, "--json", "{{project_dir}}"}, []string{root}); err != nil {
		testutil.FailErr(t, "ValidateExternalArgv abs outside", err)
	}
}

func TestValidateExternalArgvAbsProjectArgRejected(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write bin", err)
	}
	err := scancatalog.ValidateExternalArgv([]string{bin, root}, []string{root})
	if err == nil {
		t.Fatal("expected reject for absolute project path in argv")
	}
}

func TestValidateExternalArgvAbsInsideRejected(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "evil")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write bin", err)
	}
	err := scancatalog.ValidateExternalArgv([]string{bin}, []string{root})
	if err == nil {
		t.Fatal("expected reject for abs inside root")
	}
	if !strings.Contains(err.Error(), scancatalog.RejectArgvProjectPathForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateExternalArgvRelativeRejected(t *testing.T) {
	err := scancatalog.ValidateExternalArgv([]string{"./tool"}, []string{t.TempDir()})
	if err == nil {
		t.Fatal("expected relative reject")
	}
}

func TestValidateExternalArgvProjectDirInCommand0Rejected(t *testing.T) {
	err := scancatalog.ValidateExternalArgv([]string{"{{project_dir}}/" + settingsoverlay.Rel("evil")}, []string{t.TempDir()})
	if err == nil {
		t.Fatal("expected project_dir in command[0] reject")
	}
	if !strings.Contains(err.Error(), scancatalog.RejectArgvProjectPathForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateExternalArgvSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	realBin := filepath.Join(outside, "tool")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write real", err)
	}
	linkOK := filepath.Join(root, "link-out")
	if err := os.Symlink(realBin, linkOK); err != nil {
		testutil.FailErr(t, "symlink out", err)
	}
	if err := scancatalog.ValidateExternalArgv([]string{linkOK}, []string{root}); err != nil {
		testutil.FailErr(t, "symlink to outside should be ok", err)
	}

	inside := filepath.Join(root, "payload")
	if err := os.WriteFile(inside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write inside", err)
	}
	linkIn := filepath.Join(outside, "link-in")
	if err := os.Symlink(inside, linkIn); err != nil {
		testutil.FailErr(t, "symlink in", err)
	}
	err := scancatalog.ValidateExternalArgv([]string{linkIn}, []string{root})
	if err == nil {
		t.Fatal("expected symlink into project reject")
	}
}

func TestAssertBinaryOutsideRootsAtRun(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "x")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		testutil.FailErr(t, "write", err)
	}
	if err := scancatalog.AssertBinaryOutsideRoots(bin, []string{root}); err == nil {
		t.Fatal("expected reject")
	}
}

func TestResolveBinaryOutsideRootsRejectsBareNameFromWorkspacePATH(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "scanner")
	testutil.FailErr(t, "write scanner", os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", root)
	if _, err := scancatalog.ResolveBinaryOutsideRoots("scanner", []string{root}); err == nil {
		t.Fatal("expected workspace PATH binary to be rejected")
	}
}
