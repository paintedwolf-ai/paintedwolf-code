//go:build grammar_blobs_external

package grammars

import "os"

func init() {
	if os.Getenv("GOTREESITTER_GRAMMAR_BLOB_DIR") != "" {
		return
	}
	if _, err := os.Stat("grammar_blobs"); err == nil {
		_ = os.Setenv("GOTREESITTER_GRAMMAR_BLOB_DIR", "grammar_blobs")
	}
}
