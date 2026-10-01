package gotreesitter

import "testing"

func TestAdvancePointByBytes(t *testing.T) {
	tests := []struct {
		name  string
		start Point
		input string
		want  Point
	}{
		{
			name:  "single line",
			start: Point{Row: 2, Column: 4},
			input: "abcdef",
			want:  Point{Row: 2, Column: 10},
		},
		{
			name:  "one newline",
			start: Point{Row: 2, Column: 4},
			input: "abc\ndef",
			want:  Point{Row: 3, Column: 3},
		},
		{
			name:  "trailing newline",
			start: Point{Row: 2, Column: 4},
			input: "abc\n",
			want:  Point{Row: 3, Column: 0},
		},
		{
			name:  "multiple newlines",
			start: Point{Row: 2, Column: 4},
			input: "a\nbc\ndef",
			want:  Point{Row: 4, Column: 3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := advancePointByBytes(tc.start, []byte(tc.input)); got != tc.want {
				t.Fatalf("advancePointByBytes(%+v, %q) = %+v, want %+v", tc.start, tc.input, got, tc.want)
			}
		})
	}
}
