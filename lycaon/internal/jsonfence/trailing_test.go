package jsonfence_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/jsonfence"
)

func TestTrailingSplitsTheLastFencedBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, markdown, before, language, payload string
	}{
		{"json fence", "Done.\n\n```json\n{\"k\":1}\n```", "Done.\n", "json", `{"k":1}`},
		{"untagged fence", "Done.\n\n```\n{\"k\":1}\n```", "Done.\n", "", `{"k":1}`},
		{"info words after the language", "Done.\n\n```json closeout\n{}\n```", "Done.\n", "json", "{}"},
		{"tilde fence", "Done.\n\n~~~json\n{}\n~~~", "Done.\n", "json", "{}"},
		{"longer closer", "Done.\n\n```json\n{}\n`````", "Done.\n", "json", "{}"},
		{"indented fence", "Done.\n\n   ```json\n{}\n   ```", "Done.\n", "json", "{}"},
		{"trailing whitespace", "Done.\n\n```json\n{}\n```  \n\n", "Done.\n", "json", "{}"},
		{"crlf line endings", "Done.\r\n\r\n```json\r\n{}\r\n```", "Done.\n", "json", "{}"},
		{"multiline payload", "Done.\n\n```json\n{\n  \"k\": 1\n}\n```", "Done.\n", "json", "{\n  \"k\": 1\n}"},
		{"block alone", "```\n{}\n```", "", "", "{}"},
		{
			"earlier blocks closed",
			"Run:\n\n```sh\ngo test ./...\n```\n\nDone.\n\n```\n{}\n```",
			"Run:\n\n```sh\ngo test ./...\n```\n\nDone.\n", "", "{}",
		},
		{
			"quoted fence inside a longer block",
			"Example:\n\n````md\n```\nquoted\n```\n````\n\n```json\n{}\n```",
			"Example:\n\n````md\n```\nquoted\n```\n````\n", "json", "{}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before, block, ok := jsonfence.Trailing(tc.markdown)
			if !ok {
				t.Fatalf("Trailing(%q) found no block", tc.markdown)
			}
			if before != tc.before || block.Language != tc.language || block.Payload != tc.payload {
				t.Fatalf("Trailing(%q) = %q, %+v; want %q, {%q %q}", tc.markdown, before, block, tc.before, tc.language, tc.payload)
			}
		})
	}
}

func TestTrailingNeedsAClosedFinalBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, markdown string }{
		{"no fence", "Plain answer."},
		{"prose after the block", "```json\n{}\n```\n\nDone."},
		{"unclosed fence", "Done.\n\n```json\n{}"},
		{"closer of another character", "Done.\n\n```json\n{}\n~~~"},
		{"shorter closer", "Done.\n\n````json\n{}\n```"},
		{"closer with info", "Done.\n\n```json\n{}\n```json"},
		{"code indented four spaces", "Done.\n\n    ```json\n    {}\n    ```"},
		{"inline code line", "Done.\n\n```json``` and more\n{}\n```"},
		{"final fence opens a block", "Run:\n\n```sh\ngo test\n```\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if before, block, ok := jsonfence.Trailing(tc.markdown); ok {
				t.Fatalf("Trailing(%q) = %q, %+v; want no block", tc.markdown, before, block)
			}
		})
	}
}

func FuzzTrailing(f *testing.F) {
	for _, seed := range []string{
		"Done.\n\n```json\n{\"k\":1}\n```",
		"Done.\n\n```\n{}\n```",
		"````md\n```\n````\n~~~\n~~~",
		"```",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, markdown string) {
		before, block, ok := jsonfence.Trailing(markdown)
		if !ok {
			return
		}
		normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
		if !strings.HasPrefix(normalized, before) {
			t.Fatalf("before %q is not a prefix of %q", before, normalized)
		}
		if !strings.Contains(normalized[len(before):], block.Payload) {
			t.Fatalf("payload %q is not inside the trailing block of %q", block.Payload, normalized)
		}
	})
}
