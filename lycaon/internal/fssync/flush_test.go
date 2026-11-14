package fssync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAlwaysFlushesAndFileHonorsRelaxation(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := Always(f); err != nil {
		t.Fatalf("flush open file: %v", err)
	}
	if err := File(f); err != nil {
		t.Fatalf("flush through File: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := Always(f); err == nil {
		t.Fatal("flushing a closed file must fail")
	}
}
