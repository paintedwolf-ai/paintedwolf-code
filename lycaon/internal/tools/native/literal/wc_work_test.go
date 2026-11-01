package literal

import (
	"strings"
	"testing"
)

func TestWcSelectedModesPreserveOutput(t *testing.T) {
	h := newHarness(t, map[string]string{"notes": "界\tone\r\nlast", "long": strings.Repeat("x", 100), "short": "x\n"})
	for _, tc := range []struct{ flag, output string }{
		{"-c", "13 notes\n"}, {"-l", "1 notes\n"}, {"-w", "3 notes\n"},
		{"-m", "11 notes\n"}, {"-L", "11 notes\n"},
		{"-lwc", " 1  3 13 notes\n"}, {"-mL", "11 11 notes\n"},
	} {
		h.exec("", "wc", tc.flag, "notes").expect(t, tc.flag, tc.output, "", 0)
	}
	h.exec("", "wc", "-l", "long", "short").expect(t, "byte alignment", "  0 long\n  1 short\n  1 total\n", "", 0)
	for _, tc := range []struct{ flag, output string }{{"-c", "3\n"}, {"-l", "1\n"}, {"-w", "1\n"}} {
		h.exec("a\xff\n", "wc", tc.flag).expect(t, "invalid bytes "+tc.flag, tc.output, "", 0)
	}
	for _, flag := range []string{"-m", "-L"} {
		h.exec("a\xff\n", "wc", flag).expect(t, "invalid encoding "+flag, "", "wc: -: invalid UTF-8 encoding\n", 1)
	}
}
