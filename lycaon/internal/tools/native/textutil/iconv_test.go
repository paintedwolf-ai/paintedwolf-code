//go:build darwin || linux

package textutil

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestIconvConvertsBetweenCodeSets(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"iconv -f latin1 -t utf-8", "caf\xe9", "café", 0},
		{"iconv -f ISO8859-1 -t UTF8", "caf\xe9", "café", 0},
		{"iconv --from-code=ISO-8859-1 --to-code=utf-8", "caf\xe9", "café", 0},
		{"iconv -f utf-8 -t latin1", "café", "caf\xe9", 0},
		{"iconv -f cp1252 -t utf-8", "\x80 \x93q\x94", "€ “q”", 0},
		{"iconv -t utf-16", "hi", "\xff\xfeh\x00i\x00", 0},
		{"iconv -f utf-16 -t utf-8", "\xfe\xff\x00h\x00i", "hi", 0},
		{"iconv -f utf-16 -t utf-8", "\xff\xfeh\x00i\x00", "hi", 0},
		{"iconv -f utf-16 -t utf-8", "\x00h\x00i", "hi", 0},
		{"iconv -f utf-16le -t utf-8", "h\x00i\x00", "hi", 0},
		{"iconv -f utf-8 -t UTF-16BE", "hi", "\x00h\x00i", 0},
		{"iconv -t utf-32le", "A", "A\x00\x00\x00", 0},
		{"iconv -t utf-32", "A", "\xff\xfe\x00\x00A\x00\x00\x00", 0},
		{"iconv -f utf-32be", "\x00\x00\x00A", "A", 0},
		{"iconv -f utf-8 -t shift_jis", "日本", "\x93\xfa\x96\x7b", 0},
		{"iconv -f SJIS -t utf-8", "\x93\xfa\x96\x7b", "日本", 0},
		{"iconv -t ISO-2022-JP", "a日本b", "a\x1b$BF|K\\\x1b(Bb", 0},
		{"iconv -f ISO-2022-JP", "a\x1b$BF|K\\\x1b(Bb", "a日本b", 0},
		{"iconv -f ascii -t utf-8", "plain\n", "plain\n", 0},
		{"iconv", "ok \xef\xbf\xbd\n", "ok \xef\xbf\xbd\n", 0},
		{"iconv -t ascii//TRANSLIT", "café “quoted” – naïve … ﬁ 日\n", "cafe \"quoted\" - naive ... fi ?\n", 0},
		{"iconv -t latin1//TRANSLIT", "Ωé", "?\xe9", 0},
		{"iconv -f latin1", "", "", 0},
		{"iconv -f utf-16 -t utf-8", "\xfe\xff\xff\xfd", "\ufffd", 0},
		{"iconv -f utf-16le", "\xfd\xff", "\ufffd", 0},
	})
}

func TestIconvStopsAtGlibcDiagnostics(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		source, stdin, stdout, stderr string
	}{
		{"iconv -f utf-8 -t ascii", "café ok", "caf", "iconv: illegal input sequence at position 3\n"},
		{"iconv -f utf-8 -t utf-16le", "a\xffb", "a\x00", "iconv: illegal input sequence at position 1\n"},
		{"iconv -f ascii", "ab\x80", "ab", "iconv: illegal input sequence at position 2\n"},
		{"iconv -f cp1252", "a\x81", "a", "iconv: illegal input sequence at position 1\n"},
		{"iconv -f utf-8 -t latin1", "a\xc3", "a", "iconv: incomplete character or shift sequence at end of buffer\n"},
		{"iconv -c -f utf-8 -t ascii", "café ok\n", "caf ok\n", ""},
		{"iconv -f utf-8 -t ascii//IGNORE", "café ok\n", "caf ok\n", ""},
		{"iconv -c", "a\xffb", "ab", ""},
		{"iconv -f foo -t utf-8", "x", "", "iconv: conversion from `foo' is not supported\nTry 'iconv --help' or 'iconv --usage' for more information.\n"},
		{"iconv -t bar", "x", "", "iconv: conversion to `bar' is not supported\nTry 'iconv --help' or 'iconv --usage' for more information.\n"},
		{"iconv -f foo -t bar//BAD", "x", "", "iconv: conversions from `foo' and to `bar//BAD' are not supported\nTry 'iconv --help' or 'iconv --usage' for more information.\n"},
		{"iconv missing.txt", "", "", "iconv: cannot open input file `missing.txt': No such file or directory\n"},
	} {
		t.Run(c.source, func(t *testing.T) {
			r := h.run(c.source, c.stdin)
			r.expectOutput(t, c.source, c.stdout, 1)
			if r.stderr != c.stderr {
				t.Fatalf("stderr %q, want %q", r.stderr, c.stderr)
			}
		})
	}
}

// TestIconvStreamsAcrossReadBoundaries converts more than several read
// buffers of multi-byte output and keeps exact positions past them.
func TestIconvStreamsAcrossReadBoundaries(t *testing.T) {
	h := newHarness(t)
	input := strings.Repeat("caf\xe9 ", 30000)
	h.run("iconv -f latin1 -t utf-8", input).expectOutput(t, "large latin1", strings.Repeat("café ", 30000), 0)
	r := h.run("iconv -t latin1", strings.Repeat("é", 40000)+"日")
	r.expectOutput(t, "late illegal", strings.Repeat("\xe9", 40000), 1)
	if r.stderr != "iconv: illegal input sequence at position 80000\n" {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestIconvListsItsCodeSets(t *testing.T) {
	r := newHarness(t).run("iconv -l", "")
	r.expectOutput(t, "iconv -l", strings.Join(charsetNames, "\n")+"\n", 0)
	for _, name := range charsetNames {
		if _, _, _, ok := lookupCharset(name); !ok {
			t.Errorf("listed code set %s does not resolve", name)
		}
	}
}

// TestIconvFileOperandsArePinnedAndOutputPublishes: inputs are pinned under
// the execution; -o stages the destination at its own pin and publishes
// only a complete conversion.
func TestIconvFileOperandsArePinnedAndOutputPublishes(t *testing.T) {
	h := newHarness(t)
	h.seed("in.txt", "caf\xe9\n")
	h.seed("bad.txt", "caf\xc3")
	r := h.run("iconv -f latin1 -t utf-8 in.txt", "")
	r.expectOutput(t, "iconv file", "café\n", 0)
	if pins := h.pins.executions[r.execution]; len(pins) != 1 || pins[0].Path != "in.txt" || pins[0].Role != toolcontract.SourceRevision {
		t.Fatalf("pins %+v", pins)
	}
	r = h.run("iconv -f latin1 -t utf-8 -o out.txt in.txt", "")
	r.expectOutput(t, "iconv -o", "", 0)
	if got := h.read("out.txt"); got != "café\n" {
		t.Fatalf("out.txt holds %q", got)
	}
	if r.result.EffectCertainty != toolcontract.EffectCompleted || len(h.pins.executions[r.execution]) != 2 {
		t.Fatalf("iconv -o result %+v pins %+v", r.result, h.pins.executions[r.execution])
	}
	r = h.run("iconv -f utf-8 -t latin1 --output=out.txt bad.txt", "")
	r.expectOutput(t, "iconv -o incomplete", "", 1)
	if got := h.read("out.txt"); got != "café\n" {
		t.Fatalf("incomplete conversion replaced out.txt with %q", got)
	}
}

func TestTransliterationApproximates(t *testing.T) {
	for r, want := range map[rune]string{'é': "e", 'Å': "A", '’': "'", '€': "EUR", 'ß': "ss", 'ﬁ': "fi", '日': "日"} {
		if got := transliteration(r); got != want {
			t.Errorf("transliteration(%q) = %q, want %q", r, got, want)
		}
	}
	if !bytes.Equal([]byte(transliteration('ø')), []byte("o")) {
		t.Errorf("ø transliterates to %q", transliteration('ø'))
	}
}
