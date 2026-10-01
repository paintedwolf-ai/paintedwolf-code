package main

import (
	"bytes"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func ts2goMapBearingLanguage() *gotreesitter.Language {
	largeGotos := make(map[uint64]gotreesitter.StateID, 2048)
	for i := 0; i < 2048; i++ {
		key := uint64(70_000+i)<<32 | uint64(i%257)
		largeGotos[key] = gotreesitter.StateID(80_000 + i*3)
	}
	return &gotreesitter.Language{
		Name:            "ts2go_map_bearing",
		SymbolNames:     []string{"end", "identifier", "class_declaration"},
		StateCount:      90_000,
		LargeStateGotos: largeGotos,
	}
}

func TestEncodeLanguageBlobDeterministicMapBearingRoundTrip(t *testing.T) {
	lang := ts2goMapBearingLanguage()
	first, err := EncodeLanguageBlob(lang)
	if err != nil {
		t.Fatalf("EncodeLanguageBlob: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("EncodeLanguageBlob returned empty payload")
	}

	for i := 0; i < 20; i++ {
		got, err := EncodeLanguageBlob(lang)
		if err != nil {
			t.Fatalf("EncodeLanguageBlob run %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("EncodeLanguageBlob run %d differs from run 0", i)
		}
	}

	loaded, err := gotreesitter.LoadLanguage(first)
	if err != nil {
		t.Fatalf("gotreesitter.LoadLanguage: %v", err)
	}
	if loaded.Name != lang.Name {
		t.Fatalf("loaded Name = %q, want %q", loaded.Name, lang.Name)
	}
	if len(loaded.LargeStateGotos) != len(lang.LargeStateGotos) {
		t.Fatalf("loaded LargeStateGotos = %d entries, want %d", len(loaded.LargeStateGotos), len(lang.LargeStateGotos))
	}
	for key, want := range lang.LargeStateGotos {
		if got := loaded.LargeStateGotos[key]; got != want {
			t.Fatalf("loaded LargeStateGotos[%d] = %d, want %d", key, got, want)
		}
	}
}
