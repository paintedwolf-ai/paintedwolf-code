package survey

import "testing"

func TestFormatReadContent(t *testing.T) {
	got := formatReadContent([]string{"alpha", "beta"}, 10)
	want := "    10: alpha\n    11: beta"
	if got != want {
		t.Fatalf("formatReadContent = %q want %q", got, want)
	}
	if formatReadContent(nil, 1) != "" {
		t.Fatal("expected empty content for empty page")
	}
}
