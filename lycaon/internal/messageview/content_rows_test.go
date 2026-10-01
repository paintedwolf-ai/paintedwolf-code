package messageview

import (
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContentRowsPreserveTextAndBoundPayload(t *testing.T) {
	text := "happened.\n" + strings.Repeat("a complete word 🌲 ", 1000) + "\n" + strings.Repeat("z", 10000)
	runes := []rune(text)
	offsets := ContentRowOffsets(runes)
	var joined strings.Builder
	for row := 0; row < len(offsets)-1; {
		page := ContentRows(runes, offsets, nil, row, 64, 8192)
		if len(page) == 0 {
			t.Fatal("empty row page before end")
		}
		bytes := 0
		for _, value := range page {
			joined.WriteString(value.Text)
			bytes += len(value.Text)
			if utf8.RuneCountInString(value.Text) > ContentRowRunes {
				t.Fatal("unbounded row")
			}
		}
		if bytes > 8192 {
			t.Fatalf("row page bytes=%d", bytes)
		}
		row += len(page)
	}
	if joined.String() != text {
		t.Fatal("line virtualization changed text")
	}
	if string(runes[offsets[0]:offsets[1]]) != "happened.\n" {
		t.Fatal("natural line was split")
	}
	if ContentRowAt(offsets, 10) != 1 {
		t.Fatal("rune position did not resolve to its row")
	}
}

func TestReadableContentDecodesEscapesAndPreservesProvenance(t *testing.T) {
	raw := `{"content":"happened.\n🌲 [redacted]\nnext","flag":true}`
	before, _, _ := strings.Cut(raw, "[redacted]")
	at := utf8.RuneCountInString(before)
	text, spans := readableContent(raw, []api.RedactedSpan{{Field: "tool_result.content", Start: at, Length: 10, Kind: api.RedactionKindObserverMask, Source: api.RedactionSourcePolicy}})
	if !strings.Contains(text, "happened.\n🌲 [redacted]\nnext") {
		t.Fatalf("escaped or split text: %s", text)
	}
	if len(spans) != 1 || string([]rune(text)[spans[0].Start:spans[0].Start+spans[0].Length]) != "[redacted]" {
		t.Fatalf("lost provenance: %+v", spans)
	}
	escaped, _ := readableContent(`{"text":"\ud83c\udf32"}`, nil)
	if !strings.Contains(escaped, "🌲") {
		t.Fatalf("lost surrogate pair: %s", escaped)
	}
}
