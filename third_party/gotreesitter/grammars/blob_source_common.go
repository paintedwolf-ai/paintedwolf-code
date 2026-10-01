package grammars

type grammarBlob struct {
	data    []byte
	release func()
}

func (b grammarBlob) close() {
	if b.release != nil {
		b.release()
	}
}

// BlobByName returns the raw grammar blob for the named language
// (e.g. "go", "python"). Returns nil if the language blob is not found.
// Returned bytes are either legacy gzip+gob data or a versioned language-blob
// envelope. Consumers must load them with gotreesitter.LoadLanguage (or the
// equivalent grammars loader) rather than assuming a gzip header. The bytes
// remain suitable for browser-side WASM modules that use that runtime decoder.
func BlobByName(name string) []byte {
	// Resolve aliases and normalize case the same way DetectLanguageByName does.
	entry := DetectLanguageByName(name)
	if entry == nil {
		return nil
	}
	// Only blob-backed sources have an embedded .bin to serve; runtime
	// grammargen extensions (GrammarSourceGrammargen) do not.
	if entry.GrammarSource != GrammarSourceTS2GoBlob && entry.GrammarSource != GrammarSourceGrammargenBlob {
		return nil
	}
	blob, err := readGrammarBlob(entry.Name + ".bin")
	if err != nil {
		return nil
	}
	// Copy the data so the caller owns it and we can release the blob.
	data := make([]byte, len(blob.data))
	copy(data, blob.data)
	blob.close()
	return data
}
