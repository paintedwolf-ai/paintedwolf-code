package gotreesitter

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"reflect"
)

// EncodeLanguageBlob serializes lang in the runtime's stable grammar-blob
// format. Languages without LargeStateGotos retain the legacy gzip+gob wire
// representation byte-for-byte. When LargeStateGotos is populated, the map is
// removed from a shallow exported-field copy, encoded as a sorted trailer, and
// wrapped in the versioned fail-closed envelope understood by LoadLanguage.
//
// Keeping this encoder in the root package makes the format invariant shared
// by every producer, including grammargen and ts2go. The input Language is
// never mutated and can remain in concurrent use while it is encoded.
func EncodeLanguageBlob(lang *Language) ([]byte, error) {
	toEncode := lang
	var trailer []byte
	if lang != nil && len(lang.LargeStateGotos) > 0 {
		var err error
		trailer, err = EncodeLargeStateGotosTrailer(lang.LargeStateGotos)
		if err != nil {
			return nil, fmt.Errorf("encode language blob: %w", err)
		}
		clone := shallowCopyExportedLanguageFields(lang)
		clone.LargeStateGotos = nil
		toEncode = clone
	}

	var out bytes.Buffer
	gzw := gzip.NewWriter(&out)
	if err := gob.NewEncoder(gzw).Encode(toEncode); err != nil {
		_ = gzw.Close()
		return nil, fmt.Errorf("encode language blob: %w", err)
	}
	if len(trailer) > 0 {
		if _, err := gzw.Write(trailer); err != nil {
			_ = gzw.Close()
			return nil, fmt.Errorf("encode language blob: %w", err)
		}
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("finalize language blob: %w", err)
	}
	if len(trailer) == 0 {
		return out.Bytes(), nil
	}
	enveloped, err := WrapLanguageBlobEnvelope(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("finalize language blob: %w", err)
	}
	return enveloped, nil
}

// shallowCopyExportedLanguageFields returns a new Language containing exactly
// the fields gob can observe. A direct struct copy would also copy sync.Once
// and atomic cache state; mutating the original map in place would race with
// parsers already using the Language.
func shallowCopyExportedLanguageFields(lang *Language) *Language {
	src := reflect.ValueOf(lang).Elem()
	t := src.Type()
	dstPtr := reflect.New(t)
	dst := dstPtr.Elem()
	for i := 0; i < t.NumField(); i++ {
		if !t.Field(i).IsExported() {
			continue
		}
		dst.Field(i).Set(src.Field(i))
	}
	return dstPtr.Interface().(*Language)
}
