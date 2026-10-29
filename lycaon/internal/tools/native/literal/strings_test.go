package literal

import (
	"strings"
	"testing"
)

// stringsBinary mixes runs of every length around the default and -n 8
// minimums, a tab, high bytes and a trailing short run.
const stringsBinary = "ab\x00hello world\x00\x01short\x02longer string here\ttab\x80\xffhighbits\nnext line ok\n12345678\x00end"

func TestStringsFormsProduceExactBytes(t *testing.T) {
	h := newHarness(t, map[string]string{"bin": stringsBinary, "dir/": ""})
	all := "hello world\nshort\nlonger string here\ttab\nhighbits\nnext line ok\n12345678\n"
	eight := "hello world\nlonger string here\ttab\nhighbits\nnext line ok\n12345678\n"
	for _, c := range []formCase{
		{argv: "strings bin", stdout: all},
		{argv: "strings -a bin", stdout: all},
		{argv: "strings -n 8 bin", stdout: eight},
		{argv: "strings --bytes=8 bin", stdout: eight},
		{argv: "strings -8 bin", stdout: eight},
		{argv: "strings -n 20 bin", stdout: "longer string here\ttab\n"},
		{argv: "strings -t x bin", stdout: "      3 hello world\n     10 short\n     16 longer string here\ttab\n     2e highbits\n     37 next line ok\n     44 12345678\n"},
		{argv: "strings -t d -n 6 bin", stdout: "      3 hello world\n     22 longer string here\ttab\n     46 highbits\n     55 next line ok\n     68 12345678\n"},
		{argv: "strings -t o bin", stdout: "      3 hello world\n     20 short\n     26 longer string here\ttab\n     56 highbits\n     67 next line ok\n    104 12345678\n"},
		{argv: "strings -o bin", stdout: "      3 hello world\n     20 short\n     26 longer string here\ttab\n     56 highbits\n     67 next line ok\n    104 12345678\n"},
		{argv: "strings -e S bin", stdout: "hello world\nshort\nlonger string here\ttab\x80\xffhighbits\nnext line ok\n12345678\n"},
		{argv: "strings -e s -n 9 bin", stdout: "hello world\nlonger string here\ttab\nnext line ok\n"},
		{argv: "strings -f -n 12 bin", stdout: "bin: longer string here\ttab\nbin: next line ok\n"},
		{argv: "strings -f", stdin: "x\x00abcd", stdout: "{standard input}: abcd\n"},
		{argv: "strings", stdin: "abcd", stdout: "abcd\n"},
		{argv: "strings", stdin: "abc"},
		{argv: "strings -n 3", stdin: "x\x00ab\tcd", stdout: "ab\tcd\n"},
		{argv: "strings bin nope", stdout: all, stderr: "strings: 'nope': No such file\n", status: 1},
		{argv: "strings dir", stderr: "strings: Warning: 'dir' is a directory\n", status: 1},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

// TestStringsRunsMapToTheirCapture: each printed run is verbatim source
// bytes, so it maps to the exact range of the pinned file; long runs that
// stream across reads still form one range.
func TestStringsRunsMapToTheirCapture(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	h := newHarness(t, map[string]string{"bin": "\x00\x01hello\x00" + long + "\x00"})
	r := h.exec("", "strings", "-t", "d", "bin")
	r.expect(t, "strings mapped", "      2 hello\n      8 "+long+"\n", "", 0)
	if len(r.mappings) != 2 {
		t.Fatalf("mappings %+v, want one per run", r.mappings)
	}
	first, second := r.mappings[0], r.mappings[1]
	if first.SourceStartByte != 2 || first.SourceEndByte != 7 || first.OutputStartByte != 8 || first.OutputEndByte != 13 {
		t.Fatalf("first run mapping %+v", first)
	}
	if second.SourceStartByte != 8 || second.SourceEndByte != 8+int64(len(long)) || second.OutputStartByte != 22 {
		t.Fatalf("long run mapping %+v", second)
	}
}

func TestStringsRefusesInvalidValues(t *testing.T) {
	h := newHarness(t, map[string]string{"bin": stringsBinary})
	for _, argv := range []string{"strings -n 0 bin", "strings -n x bin", "strings -e l bin", "strings -t q bin"} {
		h.exec("", strings.Fields(argv)...).refused(t, argv, "COMMAND_OPERAND_INVALID")
	}
}
