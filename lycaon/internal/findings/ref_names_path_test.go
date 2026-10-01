package findings

import "testing"

func TestRefNamesPath(t *testing.T) {
	cases := []struct {
		ref, path string
		want      bool
	}{
		{"pkg/main.go:12", "pkg/main.go", true},
		{"./pkg/main.go:12,40", "pkg/main.go", true},
		{"pkg/main.go", "pkg/main.go", true},
		{"a.go:1, pkg/main.go:9", "pkg/main.go", true},
		{"pkg/other.go:12", "pkg/main.go", false},
		{"", "pkg/main.go", false},
		{"pkg/main.go:12", "", false},
		{"main.go contains the bug in pkg", "pkg/main.go", false},
	}
	for _, tc := range cases {
		if got := RefNamesPath(tc.ref, tc.path); got != tc.want {
			t.Fatalf("RefNamesPath(%q, %q) = %v want %v", tc.ref, tc.path, got, tc.want)
		}
	}
}
