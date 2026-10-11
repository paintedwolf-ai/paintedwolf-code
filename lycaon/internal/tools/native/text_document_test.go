package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

var implicitTextEncodings = testutil.SelfIdentifyingTextEncodings()

func TestNativeTextToolsPreserveSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			seedText := "alpha\nbeta\n"
			seed := testutil.EncodeTextFixture(t, seedText, encoding)
			if err := os.WriteFile(path, seed, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			ctx := nativefixture.Context(dir)

			out, err := (&surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "notes.txt"}, ctx)
			if err != nil {
				t.Fatalf("read %s: %v", encoding, err)
			}
			var read surveytools.ReadResponse
			if err := json.Unmarshal([]byte(out), &read); err != nil || read.Content != "     1: alpha\n     2: beta" {
				t.Fatalf("read output=%q err=%v", out, err)
			}

			if _, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "content": "tail\n", "append": true,
			}, ctx); err != nil {
				t.Fatalf("append %s: %v", encoding, err)
			}
			if _, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "old_string": "alpha", "new_string": "ALPHA",
			}, ctx); err != nil {
				t.Fatalf("edit %s: %v", encoding, err)
			}
			if _, err := (&ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "start_line": 2, "end_line": 2, "new_content": "BETA",
			}, ctx); err != nil {
				t.Fatalf("replace lines %s: %v", encoding, err)
			}

			wantText := "ALPHA\nBETA\ntail\n"
			want := testutil.EncodeTextFixture(t, wantText, encoding)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s bytes drifted: got=%x want=%x", encoding, got, want)
			}
			doc, _, err := textfile.Open(got, textfile.LimitsForRaw(int64(len(got))))
			if err != nil || doc.Encoding() != encoding || doc.Text() != wantText {
				t.Fatalf("open after mutation = %#v err=%v", doc, err)
			}
		})
	}
}

func TestNativeWriteOverwritePreservesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			before := testutil.EncodeTextFixture(t, "before\n", encoding)
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "content": "after 世界\n",
			}, nativefixture.Context(dir)); err != nil {
				t.Fatalf("overwrite %s: %v", encoding, err)
			}
			want := testutil.EncodeTextFixture(t, "after 世界\n", encoding)
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("overwrite %s = %x err=%v, want %x", encoding, got, err, want)
			}
		})
	}
}

func TestNativeWriteNewFileDefaultsToPlainUTF8(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "new.txt", "content": "new 世界\n",
	}, nativefixture.Context(dir)); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if string(raw) != "new 世界\n" || textfile.Classify(raw).Encoding != textfile.UTF8 {
		t.Fatalf("new file = %x classification=%#v", raw, textfile.Classify(raw))
	}
}

func TestCodeRewritePreservesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seedText := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
			seed := testutil.EncodeTextFixture(t, seedText, encoding)
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, seed, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := (&CodeRewriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "main.go", "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A)`,
			}, nativefixture.Context(dir)); err != nil {
				t.Fatalf("rewrite %s: %v", encoding, err)
			}
			wantText := "package main\n\nfunc main() {\n\tlog.Info(\"hi\")\n}\n"
			want := testutil.EncodeTextFixture(t, wantText, encoding)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s bytes drifted: got=%x want=%x", encoding, got, want)
			}
		})
	}
}

func TestCodeRewriteMultiPreservesSelfIdentifyingEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seedText := "package main\n\nfunc main() { fmt.Println(\"hi\") }\n"
			seed := testutil.EncodeTextFixture(t, seedText, encoding)
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, seed, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := (&CodeRewriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"paths": []any{"."}, "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A)`,
			}, nativefixture.Context(dir)); err != nil {
				t.Fatalf("multi rewrite %s: %v", encoding, err)
			}
			wantText := "package main\n\nfunc main() { log.Info(\"hi\") }\n"
			want := testutil.EncodeTextFixture(t, wantText, encoding)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s bytes drifted: got=%x want=%x", encoding, got, want)
			}
		})
	}
}

func TestWriteRefusesUnsupportedExistingTextWithoutChangingIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "unsupported.txt")
	before := []byte("caf\xe9\n")
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"path": "unsupported.txt", "content": "replacement\n",
	}, nativefixture.Context(dir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "READ_BINARY_DENIED" {
		t.Fatalf("err = %v, want READ_BINARY_DENIED", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("unsupported file changed: after=%x err=%v", after, readErr)
	}
}

func TestNativeMutationsPreserveControlText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want string
		run  func(*testing.T, string, tools.ToolContext) error
	}{
		{"write", "control\x1btext\n", func(t *testing.T, _ string, ctx tools.ToolContext) error {
			_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "content": "control\x1btext\n",
			}, ctx)
			return err
		}},
		{"edit", "control\x1btext\nbeta\n", func(t *testing.T, _ string, ctx tools.ToolContext) error {
			_, err := (&EditTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "old_string": "alpha", "new_string": "control\x1btext",
			}, ctx)
			return err
		}},
		{"replace-lines", "control\x1btext\nbeta\n", func(t *testing.T, _ string, ctx tools.ToolContext) error {
			_, err := (&ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "start_line": 1, "end_line": 1, "new_content": "control\x1btext",
			}, ctx)
			return err
		}},
	}
	for _, tc := range cases {
		for _, encoding := range implicitTextEncodings {
			t.Run(tc.name+"/"+encoding, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				path := filepath.Join(dir, "notes.txt")
				before := testutil.EncodeTextFixture(t, "alpha\nbeta\n", encoding)
				testutil.FailErr(t, "write fixture", os.WriteFile(path, before, 0o644))
				testutil.FailErr(t, "mutate control text", tc.run(t, path, nativefixture.Context(dir)))
				after, err := os.ReadFile(path)
				testutil.FailErr(t, "read mutated file", err)
				doc, _, err := textfile.Open(after, textfile.LimitsForRaw(int64(len(after))))
				testutil.FailErr(t, "decode mutated file", err)
				if doc.Encoding() != encoding || doc.Text() != tc.want {
					t.Fatalf("mutation encoding=%q text=%q", doc.Encoding(), doc.Text())
				}
			})
		}
	}
}

type racingContentApplyGate struct {
	path  string
	newer []byte
}

func (g racingContentApplyGate) GateApply(context.Context, string, string, *string, string, tools.ToolContext) (string, error) {
	if err := os.WriteFile(g.path, g.newer, 0o644); err != nil {
		return "", err
	}
	return "agent replacement\n", nil
}

func TestNativeMutationRejectsChangeAfterOpenAndBeforeCommit(t *testing.T) {
	t.Parallel()
	for _, encoding := range implicitTextEncodings {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			opened := testutil.EncodeTextFixture(t, "opened bytes\n", encoding)
			newer := testutil.EncodeTextFixture(t, "newer bytes\n", encoding)
			if err := os.WriteFile(path, opened, 0o644); err != nil {
				t.Fatalf("write opened fixture: %v", err)
			}
			_, err := (&WriteTool{
				Boundary: nativefixture.Boundary(t), ContentApply: racingContentApplyGate{path: path, newer: newer},
			}).Run(context.Background(), map[string]any{
				"path": "notes.txt", "content": "agent replacement\n",
			}, nativefixture.Context(dir))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "TEXT_WRITE_CONFLICT" {
				t.Fatalf("error = %v, want TEXT_WRITE_CONFLICT", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, newer) {
				t.Fatalf("conflict clobbered newer bytes: got=%x want=%x err=%v", after, newer, readErr)
			}
		})
	}
}
