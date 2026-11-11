package evidence

import "testing"

func TestConfirmNumberedWindowReadsTheHostsOwnRows(t *testing.T) {
	const good = "    10: alpha\n    11: bravo"
	obs, err := ConfirmNumberedWindow("a.go", good, "response", "delivery")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	rec := obs.Record()
	if rec.Kind != "read" || rec.Path != "a.go" || len(rec.Body) != 2 || rec.Body[1] != "bravo" || !rec.ShowedLineNumbers() {
		t.Fatalf("record = %+v", rec)
	}
	if rec.LineRanges[0] != (LineRange{Start: 10, End: 11}) {
		t.Fatalf("range = %+v", rec.LineRanges)
	}
	for _, tc := range []struct{ name, path, body string }{
		{"no path", "", good},
		{"an unnumbered row", "a.go", "plain text"},
		{"rows out of sequence", "a.go", "    10: alpha\n    12: bravo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ConfirmNumberedWindow(tc.path, tc.body, "response", "delivery"); err == nil {
				t.Fatal("confirmed a window it should refuse")
			}
		})
	}
}
