package projectpaths_test

import (
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"testing"
)

func TestWindowsPathDesignShapes(t *testing.T) {
	for _, path := range []string{`C:\source\file.txt`, `\\server\share\file.txt`, `C:/source/file.txt`, `relative\Case`, "NUL"} {
		if err := projectpaths.ValidatePlatformOperand(path, "windows"); err != nil {
			t.Errorf("valid shape %q: %v", path, err)
		}
	}
	for _, path := range []string{`C:relative`, `C:`, `\\server`, `\\?\C:\secret`, `\\.\NUL`, `file:stream`, `CON.txt`, `folder\LPT1`, `file.`, `file `, "line\nbreak", string([]byte{0xff})} {
		if err := projectpaths.ValidatePlatformOperand(path, "windows"); err == nil {
			t.Errorf("ambiguous Windows shape accepted: %q", path)
		}
	}
	// On Unix these are ordinary exact bytes, not drive/device conversions.
	for _, path := range []string{`C:relative`, `folder\LPT1`, `file `, "line\nbreak", string([]byte{0xff})} {
		if err := projectpaths.ValidatePlatformOperand(path, "linux"); err != nil {
			t.Errorf("Unix name %q: %v", path, err)
		}
	}
}
