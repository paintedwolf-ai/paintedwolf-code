package literal

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// cmpFiles are the comparison fixtures.
var cmpFiles = map[string]string{
	"a": "abc\ndef\nghi", "b": "abX\ndEf\n", "c": "abc\ndef\nghi", "e": "",
	"x": "a\x01\x80\xff", "y": "a\x02\x81\x7f",
	"n1": "ab\ncd\n", "n2": "ab\ncd\nef", "n3": "ab\ncd",
	"z1": strings.Repeat("\x00", 2000), "z3": "x" + strings.Repeat("\x00", 1999),
}

func TestCmpFormsProduceExactBytes(t *testing.T) {
	h := newHarness(t, cmpFiles)
	eofB := "cmp: EOF on 'b' after byte 8\n"
	for _, c := range []formCase{
		{argv: "cmp a b", stdout: "a b differ: char 3, line 1\n", status: 1},
		{argv: "cmp -b a b", stdout: "a b differ: byte 3, line 1 is 143 c 130 X\n", status: 1},
		{argv: "cmp --print-bytes a b", stdout: "a b differ: byte 3, line 1 is 143 c 130 X\n", status: 1},
		{argv: "cmp -l a b", stdout: "3 143 130\n6 145 105\n", stderr: eofB, status: 1},
		{argv: "cmp --verbose a b", stdout: "3 143 130\n6 145 105\n", stderr: eofB, status: 1},
		{argv: "cmp -lb a b", stdout: "3 143 c    130 X\n6 145 e    105 E\n", stderr: eofB, status: 1},
		{argv: "cmp -s a b", status: 1},
		{argv: "cmp --quiet a b", status: 1},
		{argv: "cmp a c"},
		{argv: "cmp -l a c"},
		{argv: "cmp a e", stderr: "cmp: EOF on 'e' which is empty\n", status: 1},
		{argv: "cmp e a", stderr: "cmp: EOF on 'e' which is empty\n", status: 1},
		{argv: "cmp -l a e", stderr: "cmp: EOF on 'e' which is empty\n", status: 1},
		{argv: "cmp -n 2 a b"},
		{argv: "cmp --bytes=1 a b"},
		{argv: "cmp -n 0 a b"},
		{argv: "cmp -i 1 a b", stdout: "a b differ: char 2, line 1\n", status: 1},
		{argv: "cmp -i 1:2 a b", stdout: "a b differ: char 1, line 1\n", status: 1},
		{argv: "cmp --ignore-initial=3 a b", stdout: "a b differ: char 3, line 2\n", status: 1},
		{argv: "cmp a b 1 2", stdout: "a b differ: char 1, line 1\n", status: 1},
		{argv: "cmp -i 1k a b"},
		{argv: "cmp -i 2:1 a b", stdout: "a b differ: char 1, line 1\n", status: 1},
		{argv: "cmp -b x y", stdout: "x y differ: byte 2, line 1 is   1 ^A   2 ^B\n", status: 1},
		{argv: "cmp -l x y", stdout: "2   1   2\n3 200 201\n4 377 177\n", status: 1},
		{argv: "cmp -lb x y", stdout: "2   1 ^A     2 ^B\n3 200 M-^@ 201 M-^A\n4 377 M-^? 177 ^?\n", status: 1},
		{argv: "cmp n1 n2", stderr: "cmp: EOF on 'n1' after byte 6, line 2\n", status: 1},
		{argv: "cmp n2 n1", stderr: "cmp: EOF on 'n1' after byte 6, line 2\n", status: 1},
		{argv: "cmp n3 n2", stderr: "cmp: EOF on 'n3' after byte 5, in line 2\n", status: 1},
		{argv: "cmp -l z1 z3", stdout: "   1   0 170\n", status: 1},
		{argv: "cmp -l -n 50 z1 z3", stdout: " 1   0 170\n", status: 1},
		{argv: "cmp nope a", stderr: "cmp: nope: No such file or directory\n", status: 2},
		{argv: "cmp -s nope a", status: 2},
		{argv: "cmp a nope", stderr: "cmp: nope: No such file or directory\n", status: 2},
		{argv: "cmp a", stdin: "abX\n", stdout: "a - differ: char 3, line 1\n", status: 1},
		{argv: "cmp - a", stdin: "abc\ndef\nghi"},
		{argv: "cmp -l a -", stdin: "abc\nd", stderr: "cmp: EOF on '-' after byte 5\n", status: 1},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

// TestCmpDifferencesArePublishableComparisons: a complete status-one cmp
// is a comparison result, so a redirect may publish it.
func TestCmpDifferencesArePublishableComparisons(t *testing.T) {
	h := newHarness(t, cmpFiles)
	r := h.exec("", "cmp", "a", "b")
	if !r.result.PublicationEligible || r.result.PublicationBasis != toolcontract.NativeComparisonPublication {
		t.Fatalf("cmp difference %+v", r.result)
	}
	if pins := h.pins.pins[r.execution]; len(pins) != 2 {
		t.Fatalf("cmp pinned %d inputs, want both", len(pins))
	}
	r = h.exec("", "cmp", "nope", "a")
	if r.result.PublicationEligible {
		t.Fatalf("cmp trouble is publishable %+v", r.result)
	}
}

func TestCmpRefusesInvalidForms(t *testing.T) {
	h := newHarness(t, cmpFiles)
	for _, argv := range []string{"cmp", "cmp -s -l a b", "cmp -n x a b", "cmp -i 1:z a b", "cmp a b 1 2 3", "cmp a b q"} {
		h.exec("", strings.Fields(argv)...).refused(t, argv, "COMMAND_OPERAND_INVALID")
	}
}

func TestByteCountsAcceptSizeSuffixes(t *testing.T) {
	for value, want := range map[string]int64{"0": 0, "17": 17, "0x10": 16, "010": 8, "0xb": 11, "2b": 1024, "1k": 1024, "1K": 1024, "1KiB": 1024, "1kB": 1000, "1M": 1 << 20, "1MB": 1000000, "1G": 1 << 30, "1GB": 1000000000, "1T": 1 << 40, "1TB": 1000000000000} {
		if got, ok := byteCount(value); !ok || got != want {
			t.Errorf("byteCount(%q) = %d, %v; want %d", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "x", "-1", "1Q", "1_0", "0b1", "0o7", "99999999999T", "k"} {
		if got, ok := byteCount(value); ok {
			t.Errorf("byteCount(%q) = %d, want refusal", value, got)
		}
	}
}
