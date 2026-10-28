//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
)

func main() {
	if err := commit(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func commit(root string) error {
	store := blobstore.Store{Root: root, Dir: "model-content"}
	blob, err := store.Put("reply.txt", strings.NewReader("stored model reply"), bytebound.Materialization(1024))
	if err != nil {
		return fmt.Errorf("commit model output: %w", err)
	}
	reopened := blobstore.Store{Root: root, Dir: "model-content"}
	resolved, err := reopened.Resolve(blob.ID)
	if err != nil {
		return fmt.Errorf("resolve committed output: %w", err)
	}
	reader, err := reopened.Open(resolved)
	if err != nil {
		return fmt.Errorf("open committed output: %w", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read committed output: %w", err)
	}
	if string(body) != "stored model reply" {
		return fmt.Errorf("committed output = %q", body)
	}
	return nil
}
