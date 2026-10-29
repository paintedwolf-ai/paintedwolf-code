package projectpaths_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestWindowsPathDesignShapes(t *testing.T) {
	windows := fspath.WindowsOperandNames()
	for _, path := range []string{`C:\source\file.txt`, `\\server\share\file.txt`, `C:/source/file.txt`, `relative\Case`, "NUL", "nul"} {
		if err := projectpaths.ValidateOperand(path, windows); err != nil {
			t.Errorf("valid shape %q: %v", path, err)
		}
	}
	for _, path := range []string{`C:relative`, `C:`, `1:/file`, `\rooted`, `/rooted`, `//server`, `//./C:/device`, `\\server`, `\\?\C:\secret`, `\\.\NUL`, `file:stream`, `CON.txt`, `folder\LPT1`, `file.`, `file `, "line\nbreak", string([]byte{0xff}), "", "a\x00b"} {
		if err := projectpaths.ValidateOperand(path, windows); err == nil {
			t.Errorf("ambiguous Windows shape accepted: %q", path)
		}
	}
	// On POSIX platforms these are ordinary exact bytes, not drive/device conversions.
	for _, path := range []string{`C:relative`, `folder\LPT1`, `file `, "line\nbreak", string([]byte{0xff}), "NUL"} {
		if err := projectpaths.ValidateOperand(path, fspath.LinuxOperandNames()); err != nil {
			t.Errorf("POSIX name %q: %v", path, err)
		}
	}
}

func TestUTF8NamesRefuseNonUnicodeNativeRepresentation(t *testing.T) {
	if err := projectpaths.ValidateOperand(string([]byte{'n', 0xff}), fspath.DarwinOperandNames()); err == nil {
		t.Fatal("a UTF-8 platform accepted an unrepresentable byte name")
	}
	for _, path := range []string{"é", "e\u0301", "Case", "case", "line\nbreak"} {
		if err := projectpaths.ValidateOperand(path, fspath.DarwinOperandNames()); err != nil {
			t.Fatalf("representable exact name %q refused: %v", path, err)
		}
	}
}

// The null device follows the declared facts: /dev/null everywhere, and a
// platform's own device name only where it declares one.
func TestNullOperandFollowsDeclaredDevice(t *testing.T) {
	ownDevice := fspath.OperandNames{Grammar: fspath.POSIXNames, NullDevice: "NUL"}
	cases := []struct {
		names    fspath.OperandNames
		operand  string
		expected bool
	}{
		{fspath.LinuxOperandNames(), "/dev/null", true},
		{fspath.LinuxOperandNames(), "NUL", false},
		{fspath.WindowsOperandNames(), "nul", true},
		{fspath.WindowsOperandNames(), "/dev/null", true},
		{ownDevice, "Nul", true},
		{ownDevice, "null", false},
	}
	for _, tc := range cases {
		if got := projectpaths.NullOperand(tc.operand, tc.names); got != tc.expected {
			t.Errorf("%q under %+v = %v", tc.operand, tc.names, got)
		}
	}
	if projectpaths.IsNullOperand("/dev/null") != true || projectpaths.ValidateNativeOperand("/dev/null") != nil {
		t.Fatal("host facts refuse /dev/null")
	}
}
