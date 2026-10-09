//go:build integration

package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestExtractArchiveToolRejectsEntryLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		suffix string
		write  func(*testing.T, string, map[string]string)
	}{{".zip", writeTestZip}, {".tar.gz", writeTestTarGz}} {
		t.Run(tc.suffix, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			zipPath := filepath.Join(tmpDir, "many"+tc.suffix)
			entries := make(map[string]string, hostExtractMaxEntries+1)
			for i := range hostExtractMaxEntries + 1 {
				entries[fmt.Sprintf("f%d.txt", i)] = "x"
			}
			tc.write(t, zipPath, entries)
			tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path": "many" + tc.suffix,
				"dest": "out",
			}, nativefixture.Context(tmpDir))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "EXTRACT_ENTRY_LIMIT" {
				t.Fatalf("err = %v want EXTRACT_ENTRY_LIMIT", err)
			}
			files, err := os.ReadDir(filepath.Join(tmpDir, "out"))
			testutil.FailErr(t, "read partial extraction", err)
			if len(files) != hostExtractMaxEntries {
				t.Fatalf("wrote %d entries, cap %d", len(files), hostExtractMaxEntries)
			}
			paths, ok := reject.Data["extracted_paths"].([]string)
			if !ok || len(paths) != hostExtractMaxEntries || reject.Data["extracted_entries"] != hostExtractMaxEntries || reject.Data["extracted_bytes"] != int64(hostExtractMaxEntries) {
				t.Fatalf("partial extraction facts = %#v", reject.Data)
			}
			for _, path := range paths {
				if !strings.HasPrefix(path, "out/") {
					t.Fatalf("extracted path missing destination: %q", path)
				}
			}

		})
	}
}
