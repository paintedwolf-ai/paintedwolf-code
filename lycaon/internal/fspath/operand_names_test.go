package fspath

import (
	"runtime"
	"testing"
)

// Each platform's declared operand facts are pinned; any other platform keeps
// exact POSIX bytes.
func TestPlatformDeclaresItsOperandNames(t *testing.T) {
	declared := map[string]OperandNames{
		"linux":   {Grammar: POSIXNames},
		"darwin":  {Grammar: POSIXNames, UTF8: true},
		"windows": {Grammar: WindowsNames, UTF8: true, NullDevice: "NUL"},
	}
	if LinuxOperandNames() != declared["linux"] || DarwinOperandNames() != declared["darwin"] || WindowsOperandNames() != declared["windows"] {
		t.Fatalf("declared sets changed: %+v %+v %+v", LinuxOperandNames(), DarwinOperandNames(), WindowsOperandNames())
	}
	want, ok := declared[runtime.GOOS]
	if !ok {
		want = declared["linux"]
	}
	if got := HostOperandNames(); got != want {
		t.Fatalf("%s reports %+v, want %+v", runtime.GOOS, got, want)
	}
}
