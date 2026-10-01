package ptyinput

import (
	"strings"
	"testing"
)

func TestDecodeLinesRendersTypedCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"single command", "aws s3 rb s3://old{Enter}", []string{"aws s3 rb s3://old"}},
		{"literal newline", "cd /tmp\naws s3 rb s3://old\n", []string{"cd /tmp", "aws s3 rb s3://old"}},
		{"no trailing enter", "aws s3 rb s3://old", []string{"aws s3 rb s3://old"}},
		{"answering a prompt", "y{Enter}", []string{"y"}},
		{"erase", "aws s3 lss{Backspace}{Enter}", []string{"aws s3 ls"}},
		{"kill line", "aws s3 rb s3://old{Ctrl-U}aws s3 ls{Enter}", []string{"aws s3 ls"}},
		{"word erase", "aws s3 rb s3://old{Ctrl-W}s3://new{Enter}", []string{"aws s3 rb s3://new"}},
		{"interrupt discards", "aws s3 rb s3://old{Ctrl-C}", nil},
		{"arrows carry no text", "{Up}{Down}ls{Enter}", []string{"ls"}},
		{"empty", "", nil},
		{"enter only", "{Enter}", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := DecodeLines(c.input)
			if len(got) != len(c.want) {
				t.Fatalf("DecodeLines(%q) = %q, want %q", c.input, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("DecodeLines(%q) = %q, want %q", c.input, got, c.want)
				}
			}
		})
	}
}

// An unknown token rejects at the handler. The projection still renders the text
// around it, so a payload cannot dodge matching by appending one bad token.
func TestDecodeLinesRendersAroundUnknownToken(t *testing.T) {
	t.Parallel()
	got := DecodeLines("aws s3 rb s3://old{F1}")
	if len(got) != 1 || !strings.HasPrefix(got[0], "aws s3 rb s3://old") {
		t.Fatalf("DecodeLines = %q, want the literal text preserved", got)
	}
}

func TestDecodeLinesBoundsOutput(t *testing.T) {
	t.Parallel()
	if got := DecodeLines(strings.Repeat("ls\n", maxLines+50)); len(got) != maxLines {
		t.Fatalf("line count = %d, want %d", len(got), maxLines)
	}
	got := DecodeLines(strings.Repeat("a", maxLineBytes+500))
	if len(got) != 1 || len(got[0]) != maxLineBytes {
		t.Fatalf("line bytes = %d, want %d", len(got[0]), maxLineBytes)
	}
}
