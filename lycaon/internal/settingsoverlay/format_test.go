package settingsoverlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFormatMissingIsOne(t *testing.T) {
	root := t.TempDir()
	format, err := ReadFormat(root)
	if err != nil {
		t.Fatalf("ReadFormat: %v", err)
	}
	if format != 1 {
		t.Fatalf("format = %d want 1", format)
	}
	if err := CheckFormat(root); err != nil {
		t.Fatalf("CheckFormat missing: %v", err)
	}
}

func TestCheckFormatRejectsInvalidAndNewerValues(t *testing.T) {
	for _, row := range []struct {
		name string
		body string
		want error
	}{
		{name: "newer", body: fmt.Sprintf("overlay_format: %d\n", MaxFormat+1), want: ErrFormatTooNew},
		{name: "missing", body: "other: true\n", want: ErrFormatInvalid},
		{name: "multiple documents", body: "overlay_format: 1\n---\nother: true\n", want: ErrFormatInvalid},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			writeFormat(t, root, row.body)
			if err := CheckFormat(root); !errors.Is(err, row.want) {
				t.Fatalf("CheckFormat error = %v want %v", err, row.want)
			}
		})
	}
}

func TestFormatPath(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, DirName(), FormatFileName)
	if got := FormatPath(root); got != want {
		t.Fatalf("FormatPath = %q want %q", got, want)
	}
	if FormatPath("") != "" {
		t.Fatal("empty root")
	}
}

func writeFormat(t *testing.T, root, body string) {
	t.Helper()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FormatFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write overlay format: %v", err)
	}
}
