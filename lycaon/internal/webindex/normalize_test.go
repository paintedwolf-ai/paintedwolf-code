package webindex

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNormalizeWebTextForStorage(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
		noLT   bool
	}{
		{
			name: "tags and entities",
			in:   `<p>Hello&nbsp;<b>world</b></p>`,
			want: "Hello world",
		},
		{
			name: "script dropped",
			in:   `<div>Visible<script>alert(1)</script> end</div>`,
			want: "Visible end",
		},
		{
			name: "whitespace collapse",
			in:   "  a\n\tb  ",
			want: "a b",
		},
		{
			name: "control chars",
			in:   "ok\x00\x01there",
			want: "okthere",
		},
		{
			name: "zero width",
			in:   "ab\u200Bcd",
			want: "abcd",
		},
		{
			name: "markup discussing tags loses angle brackets",
			in:   `use the <div> element`,
			want: "use the element",
			noLT: true,
		},
		{
			name:   "truncate",
			in:     "abcdefghij",
			maxLen: 5,
			want:   "abcde",
		},
		{
			name: "plain json-ish snippet",
			in:   `{"title":"Plain"}`,
			want: `{"title":"Plain"}`,
		},
		{
			name: "bidi controls",
			in:   "ab\u202Ecd",
			want: "abcd",
		},
		{
			name: "meta entities",
			in:   "Foo &amp; Bar",
			want: "Foo & Bar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeWebTextForStorage(tc.in, tc.maxLen)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if tc.noLT && strings.Contains(got, "<") {
				t.Fatalf("expected no '<': %q", got)
			}
		})
	}
}

func TestQueuePageNormalizesTitleAndDescription(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{
		URL:         "https://example.com/p",
		Title:       `<b>Title&amp;More</b>`,
		Description: `<script>x</script>Desc`,
		Verified:    true,
	})
	s.Flush()
	docs, err := s.Search(context.Background(), "Title More", 5)
	testutil.FailErr(t, "search", err)
	if len(docs) == 0 {
		t.Fatal("expected hit")
	}
	if strings.Contains(docs[0].Title, "<") || strings.Contains(docs[0].Description, "<") {
		t.Fatalf("persisted markup: title=%q desc=%q", docs[0].Title, docs[0].Description)
	}
	if docs[0].Title != "Title&More" {
		t.Fatalf("title = %q want Title&More", docs[0].Title)
	}
	if docs[0].Description != "Desc" {
		t.Fatalf("description = %q want Desc", docs[0].Description)
	}
}

func TestQueueAnchorsNormalizesMarkup(t *testing.T) {
	s := openTest(t)
	url := "https://example.com/anchors"
	s.QueuePage(t.Context(), Page{URL: url, Title: "page", Verified: true})
	s.QueueAnchors(t.Context(), url, []string{`<b>Steam&amp;Machine</b>`, "plain link"}, OriginEarned)
	s.Flush()
	var text string
	testutil.FailErr(t, "read anchors", s.db.QueryRow(
		`SELECT text FROM anchors WHERE url = ? AND text LIKE 'Steam%'`, url,
	).Scan(&text))
	if text != "Steam&Machine" {
		t.Fatalf("anchor text = %q", text)
	}
	if strings.Contains(text, "<") {
		t.Fatalf("anchor still has markup: %q", text)
	}
}
