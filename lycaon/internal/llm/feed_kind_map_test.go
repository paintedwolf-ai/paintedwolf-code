package llm

import (
	"testing"
)

func TestCatalogAuthoritative(t *testing.T) {
	if !CatalogAuthoritative("openai", true) {
		t.Fatal("mapped + usable should be authoritative")
	}
	if CatalogAuthoritative("openai", false) {
		t.Fatal("mapped + unavailable must not be authoritative")
	}
	if CatalogAuthoritative("ollama", true) {
		t.Fatal("unmapped must not be authoritative")
	}
}
