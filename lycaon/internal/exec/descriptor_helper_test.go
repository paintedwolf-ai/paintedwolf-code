//go:build unix

package exec

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestDescriptorHelperInheritsDescriptorThreeAndNamedEnvironment(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = read.Close() }()
	var stderr bytes.Buffer
	wait, err := StartDescriptorHelper(t.Context(), HostLaunch("fuse_mount"), "/bin/sh",
		[]string{"-c", `printf '%s' "$NAMED" >&3; printf diagnostic >&2`}, []string{"NAMED=value"}, write, &stderr)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = write.Close()
	if err := wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	got := make([]byte, 16)
	n, _ := read.Read(got)
	if string(got[:n]) != "value" || !strings.Contains(stderr.String(), "diagnostic") {
		t.Fatalf("descriptor %q stderr %q", got[:n], stderr.String())
	}
}

func TestDescriptorHelperRefusesAnUnattributedLaunch(t *testing.T) {
	if _, err := StartDescriptorHelper(t.Context(), LaunchPlan{}, "/bin/sh", nil, nil, os.Stdin, nil); err == nil {
		t.Fatal("a launch without a plan must be refused")
	}
}
