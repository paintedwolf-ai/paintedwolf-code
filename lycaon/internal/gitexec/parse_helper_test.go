package gitexec

import "testing"

func TestParseCredentialHelperFormats(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"credential.helper osxkeychain\n", "osxkeychain"},
		{"credential.helper=osxkeychain\n", "osxkeychain"},
		{"credential.helper store\n", "store"},
		{"credential.username bob\n", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseCredentialHelper([]byte(tc.in)); got != tc.want {
			t.Errorf("parse(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
