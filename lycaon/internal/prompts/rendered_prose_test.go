package prompts

import (
	"strings"
	"testing"
)

func TestRenderedProseStructureDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "clean prose renders no drift",
			text: "# Title\n\nFirst paragraph.\n\n1. one\n2. two\n3. three\n\n| A | B |\n| --- | --- |\n| a | b |\n",
		},
		{
			name: "single trailing newline is a terminator, not a blank line",
			text: "Paragraph.\n\n1. one\n2. two\n",
		},
		{
			name: "one trailing blank line is the PC03-009 defect",
			text: "Paragraph.\n\n",
			want: []string{"line 2: blank line at EOF (single final newline expected)"},
		},
		{
			name: "trailing blank after a list is the PC03-009 defect",
			text: "1. one\n2. two\n\n",
			want: []string{"line 3: blank line at EOF (single final newline expected)"},
		},
		{
			name: "blank line at EOF inside an open fence stays literal",
			text: "```\ncode\n\n",
		},
		{
			name: "run of blank lines is reported once",
			text: "Paragraph.\n\n\n\nNext.\n",
			want: []string{"line 3: run of blank lines"},
		},
		{
			name: "two trailing blank lines are a run",
			text: "Paragraph.\n\n\n",
			want: []string{"line 3: run of blank lines"},
		},
		{
			name: "adjacent ordered items must increment by one",
			text: "1. one\n3. three\n",
			want: []string{"line 2: ordered list numbering jumps from 1 to 3"},
		},
		{
			name: "separate lists and deeper indents do not false-positive",
			text: "1. one\n\n1. two\n\n  1. sub\n  2. sub\n10. ten\n",
		},
		{
			name: "blank line after a table separator row is reported",
			text: "| A | B |\n| --- | --- |\n\n| a | b |\n",
			want: []string{"line 3: blank line after a table separator row"},
		},
		{
			name: "fence interiors are literal",
			text: "```\n1. one\n3. three\n\n\n```\n1. one\n2. two\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderedProseStructureDrift(tt.text)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("RenderedProseStructureDrift() = %q, want %q", got, tt.want)
			}
		})
	}
}
