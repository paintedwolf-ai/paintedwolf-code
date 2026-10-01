package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCorpusDecoderRejectsHiddenDocumentsAndUnknownFields(t *testing.T) {
	for _, source := range []string{
		"language: python\n---\nlanguage: javascript\n",
		"language: python\n---\n",
		"language: python\nunreviewed_sources: true\n",
	} {
		var decoded suite
		if err := decodeCorpus([]byte(source), &decoded); err == nil {
			t.Fatalf("accepted ambiguous corpus: %q", source)
		}
	}
	var decoded suite
	testutil.FailErr(t, "decode single documented corpus", decodeCorpus([]byte("language: python\n# trailing comment\n"), &decoded))
}
