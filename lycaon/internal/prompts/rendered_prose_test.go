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
			name: "one trailing blank line is not a run",
			text: "Paragraph.\n\n",
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderedProseStructureDrift(tt.text)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("RenderedProseStructureDrift() = %q, want %q", got, tt.want)
			}
		})
	}
}
