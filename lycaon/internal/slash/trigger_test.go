package slash

import "testing"

func TestParseCommand(t *testing.T) {
	cases := []struct {
		text   string
		want   string
		wantOK bool
	}{
		{"/plan", "/plan", true},
		{"/help", "/help", true},
		{"/status extra", "/status", true},
		{"  /status  ", "/status", true},
		{"hello", "", false},
		{"/", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseCommand(tc.text)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("ParseCommand(%q) = %q, %v want %q, %v", tc.text, got, ok, tc.want, tc.wantOK)
		}
	}
}
