package naming

import "testing"

func TestNormalizeDisplayTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "trim", in: "  Ship it  ", want: "Ship it"},
		{name: "empty", in: "   ", wantErr: true},
		{name: "newline", in: "bad\ntitle", wantErr: true},
		{name: "cr", in: "bad\rtitle", wantErr: true},
		{name: "nul", in: "bad\x00title", wantErr: true},
		{name: "too long", in: string([]rune(repeatRune('x', MaxDisplayTitleRunes+1))), wantErr: true},
		{name: "max ok", in: string([]rune(repeatRune('x', MaxDisplayTitleRunes))), want: string([]rune(repeatRune('x', MaxDisplayTitleRunes)))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeDisplayTitle(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func repeatRune(r rune, n int) []rune {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return out
}
