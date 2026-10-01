package gotreesitter

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"testing"
)

// TestLoadLanguageDecodesOldFormatLargeStateGotos simulates a blob produced
// by the pre-fix encoder: LargeStateGotos gob-encoded directly as a struct
// field, with no trailer. LoadLanguage must still restore it exactly via
// gob's native map decode -- backward compatibility for all 206 blobs
// checked in before this change, including c_sharp.bin.
func TestLoadLanguageDecodesOldFormatLargeStateGotos(t *testing.T) {
	want := buildSyntheticLargeStateGotos(300)
	lang := &Language{
		Name:            "old_format_csharp_like",
		SymbolNames:     []string{"end"},
		LargeStateGotos: want,
	}

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gzw).Encode(lang); err != nil {
		t.Fatalf("Encode (simulating pre-fix encoder): %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	loaded, err := LoadLanguage(buf.Bytes())
	if err != nil {
		t.Fatalf("LoadLanguage: %v", err)
	}
	if loaded.Name != lang.Name {
		t.Fatalf("Name = %q, want %q", loaded.Name, lang.Name)
	}
	if len(loaded.LargeStateGotos) != len(want) {
		t.Fatalf("LargeStateGotos has %d entries, want %d", len(loaded.LargeStateGotos), len(want))
	}
	for k, v := range want {
		if loaded.LargeStateGotos[k] != v {
			t.Fatalf("LargeStateGotos[%d] = %d, want %d", k, loaded.LargeStateGotos[k], v)
		}
	}
}

func TestLoadLanguageRejectsUnenvelopedLargeStateGotosTrailer(t *testing.T) {
	trailer, err := EncodeLargeStateGotosTrailer(buildSyntheticLargeStateGotos(4))
	if err != nil {
		t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
	}
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gzw).Encode(&Language{Name: "unenveloped"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := gzw.Write(trailer); err != nil {
		t.Fatalf("write trailer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	if _, err := LoadLanguage(buf.Bytes()); err == nil {
		t.Fatal("LoadLanguage accepted a trailer without its versioned envelope")
	}
}

func TestLoadLanguageRejectsEnvelopeWithoutLargeStateGotosTrailer(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gzw).Encode(&Language{Name: "missing_trailer"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	enveloped, err := WrapLanguageBlobEnvelope(buf.Bytes())
	if err != nil {
		t.Fatalf("WrapLanguageBlobEnvelope: %v", err)
	}

	if _, err := LoadLanguage(enveloped); err == nil {
		t.Fatal("LoadLanguage accepted an envelope without its required trailer")
	}
}

// TestLoadLanguageDecodesNewFormatTrailer simulates a blob produced by the
// fixed encoder: LargeStateGotos cleared before the gob message, with a
// deterministic trailer appended inside the same compressed stream and a
// fail-closed version envelope outside gzip.
// LoadLanguage must reconstruct LargeStateGotos from the trailer.
func TestLoadLanguageDecodesNewFormatTrailer(t *testing.T) {
	want := buildSyntheticLargeStateGotos(300)
	lang := &Language{
		Name:        "new_format_csharp_like",
		SymbolNames: []string{"end"},
		// LargeStateGotos intentionally left nil -- the fixed encoder never
		// hands a populated LargeStateGotos to gob directly.
	}

	trailer, err := EncodeLargeStateGotosTrailer(want)
	if err != nil {
		t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
	}
	if len(trailer) == 0 {
		t.Fatal("expected non-empty trailer")
	}

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gzw).Encode(lang); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := gzw.Write(trailer); err != nil {
		t.Fatalf("write trailer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	enveloped, err := WrapLanguageBlobEnvelope(buf.Bytes())
	if err != nil {
		t.Fatalf("WrapLanguageBlobEnvelope: %v", err)
	}
	if _, err := gzip.NewReader(bytes.NewReader(enveloped)); err == nil {
		t.Fatal("pre-envelope gzip-first runtime accepted a trailer-bearing blob")
	}

	loaded, err := LoadLanguage(enveloped)
	if err != nil {
		t.Fatalf("LoadLanguage: %v", err)
	}
	if loaded.Name != lang.Name {
		t.Fatalf("Name = %q, want %q", loaded.Name, lang.Name)
	}
	if len(loaded.LargeStateGotos) != len(want) {
		t.Fatalf("LargeStateGotos has %d entries, want %d", len(loaded.LargeStateGotos), len(want))
	}
	for k, v := range want {
		if loaded.LargeStateGotos[k] != v {
			t.Fatalf("LargeStateGotos[%d] = %d, want %d", k, loaded.LargeStateGotos[k], v)
		}
	}
}

// TestLoadLanguageWithoutLargeStateGotosUnaffected pins that a Language whose
// LargeStateGotos was never populated decodes identically regardless of the
// trailer mechanism -- the vast majority of blobs (everything but c_sharp
// today).
func TestLoadLanguageWithoutLargeStateGotosUnaffected(t *testing.T) {
	lang := &Language{
		Name:        "java_like",
		SymbolNames: []string{"end", "identifier"},
	}

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	if err := gob.NewEncoder(gzw).Encode(lang); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	loaded, err := LoadLanguage(buf.Bytes())
	if err != nil {
		t.Fatalf("LoadLanguage: %v", err)
	}
	if loaded.Name != lang.Name {
		t.Fatalf("Name = %q, want %q", loaded.Name, lang.Name)
	}
	if len(loaded.LargeStateGotos) != 0 {
		t.Fatalf("LargeStateGotos = %v, want empty", loaded.LargeStateGotos)
	}
}
