package surveyjson

import (
	"strings"
	"testing"
)

func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	raw, err := Marshal(map[string]string{"content": "<foo> & bar"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `\u003c`) || strings.Contains(s, `\u0026`) {
		t.Fatalf("unexpected unicode escapes: %s", s)
	}
	if !strings.Contains(s, "<foo>") || !strings.Contains(s, "& bar") {
		t.Fatalf("want literal < and &: %s", s)
	}
}

func TestMarshalIndentDoesNotEscapeHTML(t *testing.T) {
	raw, err := MarshalIndent(map[string]string{"content": "<foo> & bar"}, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, `\u003c`) || strings.Contains(s, `\u0026`) {
		t.Fatalf("unexpected unicode escapes: %s", s)
	}
	if !strings.Contains(s, "<foo>") || !strings.Contains(s, "& bar") {
		t.Fatalf("want literal < and &: %s", s)
	}
}
