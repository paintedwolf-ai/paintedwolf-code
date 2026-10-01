package catalog_test

import (
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExpandCommandProjectDirToken(t *testing.T) {
	argv, err := scancatalog.ExpandCommand([]string{"scan", "{{project_dir}}"}, "/tmp/project", "/tmp/project/src", "")
	testutil.FailErr(t, "scan.ExpandCommand failed", err)
	if argv[1] != "/tmp/project" {
		t.Fatalf("argv = %v", argv)
	}
}

func TestExpandCommandRejectsUnknownToken(t *testing.T) {
	_, err := scancatalog.ExpandCommand([]string{"{{unknown}}"}, "/tmp/p", "/tmp/p", "")
	if err == nil {
		t.Fatal("expected unknown token error")
	}
}

func TestExpandCommandScanTargetToken(t *testing.T) {
	argv, err := scancatalog.ExpandCommand([]string{"scan", "dir:{{scan_target}}"}, "/tmp/project", "/tmp/project/pkg", "")
	testutil.FailErr(t, "scan.ExpandCommand failed", err)
	if argv[1] != "dir:/tmp/project/pkg" {
		t.Fatalf("argv = %v", argv)
	}
}
