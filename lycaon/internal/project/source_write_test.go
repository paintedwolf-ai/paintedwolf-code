package project

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func writeTestProject(t *testing.T, files map[string]string) (*Project, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(abs, []byte(content), 0o644))
	}
	return &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}, root
}

func TestWriteProjectSourceUTF8BOMByteIdentical(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	disk := testutil.EncodeTextFixture(t, "line one\nline two\n", textfile.UTF8BOM)
	abs := filepath.Join(root, "bom.txt")
	testutil.FailErr(t, "write", os.WriteFile(abs, disk, 0o644))
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}

	read, err := ReadProjectSource(p, SourceReadRequest{Path: "bom.txt"})
	testutil.FailErr(t, "read", err)
	_, err = writeProjectSource(p, SourceWriteRequest{
		Path:       "bom.txt",
		Content:    read.Content,
		Encoding:   read.Encoding,
		BaseSHA256: read.SHA256,
	})
	testutil.FailErr(t, "put unmodified", err)
	after, err := os.ReadFile(abs)
	testutil.FailErr(t, "read back", err)
	if string(after) != string(disk) {
		t.Fatalf("byte drift: before=%q after=%q", disk, after)
	}

	_, err = writeProjectSource(p, SourceWriteRequest{
		Path:       "bom.txt",
		Content:    "line one edited\nline two\n",
		Encoding:   textfile.UTF8BOM,
		BaseSHA256: textfile.SHA256(disk),
	})
	testutil.FailErr(t, "edit", err)
	edited, err := os.ReadFile(abs)
	testutil.FailErr(t, "read edited", err)
	want := testutil.EncodeTextFixture(t, "line one edited\nline two\n", textfile.UTF8BOM)
	if string(edited) != string(want) {
		t.Fatalf("edited = %q want %q", edited, want)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "bom.txt",
		Content:    "x\n",
		Encoding:   "windows-1252",
		BaseSHA256: textfile.SHA256(edited),
	}); !errors.Is(err, ErrSourceEncodingInvalid) {
		t.Fatalf("err = %v want encoding invalid", err)
	}
}

func TestProjectSourceEditorRoundTripsEverySupportedEncoding(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{
		textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LE,
		textfile.UTF16LEBOM, textfile.UTF16BE, textfile.UTF16BEBOM,
	} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "document.txt")
			originalText := "first line\n世界 \U0001F43A\n"
			originalRaw := testutil.EncodeTextFixture(t, originalText, encoding)
			testutil.FailErr(t, "write original", os.WriteFile(path, originalRaw, 0o644))
			p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}

			readReq := SourceReadRequest{Path: "document.txt"}
			if encoding == textfile.UTF16LE || encoding == textfile.UTF16BE {
				readReq.DecodeAs = encoding
			}
			opened, err := ReadProjectSource(p, readReq)
			testutil.FailErr(t, "open original", err)
			if opened.Encoding != encoding || opened.Content != originalText || opened.SHA256 != textfile.SHA256(originalRaw) {
				t.Fatalf("opened = encoding=%q content=%q sha=%q", opened.Encoding, opened.Content, opened.SHA256)
			}
			unchanged, err := writeProjectSource(p, SourceWriteRequest{
				Path: "document.txt", Content: opened.Content,
				Encoding: opened.Encoding, BaseSHA256: opened.SHA256,
			})
			testutil.FailErr(t, "save unchanged", err)
			if unchanged.SHA256 != opened.SHA256 {
				t.Fatalf("unchanged sha = %q, want %q", unchanged.SHA256, opened.SHA256)
			}
			afterUnchanged, err := os.ReadFile(path)
			testutil.FailErr(t, "read unchanged", err)
			if !bytes.Equal(afterUnchanged, originalRaw) {
				t.Fatalf("unchanged save drifted bytes: before=%x after=%x", originalRaw, afterUnchanged)
			}

			editedText := "first line edited\n世界 \U0001F43A\n"
			edited, err := writeProjectSource(p, SourceWriteRequest{
				Path: "document.txt", Content: editedText,
				Encoding: encoding, BaseSHA256: unchanged.SHA256,
			})
			testutil.FailErr(t, "save edited", err)
			wantRaw := testutil.EncodeTextFixture(t, editedText, encoding)
			gotRaw, err := os.ReadFile(path)
			testutil.FailErr(t, "read edited", err)
			if !bytes.Equal(gotRaw, wantRaw) || edited.SHA256 != textfile.SHA256(wantRaw) {
				t.Fatalf("edited save drift: got=%x result=%+v want=%x", gotRaw, edited, wantRaw)
			}
			reopened, err := ReadProjectSource(p, readReq)
			testutil.FailErr(t, "reopen edited", err)
			if reopened.Encoding != encoding || reopened.Content != editedText || reopened.SHA256 != edited.SHA256 {
				t.Fatalf("reopened = %+v", reopened)
			}
		})
	}
}

func TestProjectSourceEditorRejectsEncodingMismatchWithoutChangingBytes(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{
		textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LE,
		textfile.UTF16LEBOM, textfile.UTF16BE, textfile.UTF16BEBOM,
	} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "document.txt")
			before := testutil.EncodeTextFixture(t, "original\n", encoding)
			testutil.FailErr(t, "write fixture", os.WriteFile(path, before, 0o644))
			p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
			wrong := textfile.UTF8
			if encoding == textfile.UTF8 {
				wrong = textfile.UTF16BEBOM
			}
			_, err := writeProjectSource(p, SourceWriteRequest{
				Path: "document.txt", Content: "replacement\n",
				Encoding: wrong, BaseSHA256: textfile.SHA256(before),
			})
			if !errors.Is(err, ErrSourceEncodingInvalid) {
				t.Fatalf("error = %v, want ErrSourceEncodingInvalid", err)
			}
			after, readErr := os.ReadFile(path)
			testutil.FailErr(t, "read refused save", readErr)
			if !bytes.Equal(after, before) {
				t.Fatalf("mismatched encoding changed bytes: before=%x after=%x", before, after)
			}
		})
	}
}

func TestWriteProjectSourceRoundTrip(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{"src/hello.go": "package main\n"})

	read, err := ReadProjectSource(p, SourceReadRequest{Path: "src/hello.go"})
	testutil.FailErr(t, "read", err)
	if read.SHA256 == "" {
		t.Fatal("read sha256 empty for non-truncated file")
	}

	got, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "src/hello.go",
		Content:    "package main\n\nfunc main() {}\n",
		Encoding:   read.Encoding,
		BaseSHA256: read.SHA256,
	})
	testutil.FailErr(t, "write", err)
	if got.Path != "src/hello.go" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.SHA256 != textfile.SHA256([]byte("package main\n\nfunc main() {}\n")) {
		t.Fatalf("sha = %q", got.SHA256)
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "src", "hello.go"))
	testutil.FailErr(t, "read back", err)
	if string(onDisk) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("on disk = %q", onDisk)
	}

	// The reported sha is a valid base for a follow-up save.
	_, err = writeProjectSource(p, SourceWriteRequest{
		Path:       "src/hello.go",
		Content:    "package main\n",
		Encoding:   textfile.UTF8,
		BaseSHA256: got.SHA256,
	})
	testutil.FailErr(t, "second write", err)
}

func TestWriteProjectSourcePreservesMode(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)
	abs := filepath.Join(root, "run.sh")
	testutil.FailErr(t, "write exec fixture", os.WriteFile(abs, []byte("#!/bin/sh\n"), 0o755))

	_, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "run.sh",
		Content:    "#!/bin/sh\necho hi\n",
		Encoding:   textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("#!/bin/sh\n")),
	})
	testutil.FailErr(t, "write", err)
	info, err := os.Stat(abs)
	testutil.FailErr(t, "stat", err)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v want 0755", info.Mode().Perm())
	}
}

func TestWriteProjectSourceConflict(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, map[string]string{"a.txt": "one\n"})

	read, err := ReadProjectSource(p, SourceReadRequest{Path: "a.txt"})
	testutil.FailErr(t, "read", err)

	// Concurrent change lands between read and save.
	testutil.FailErr(t, "mutate", os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644))

	_, err = writeProjectSource(p, SourceWriteRequest{
		Path:       "a.txt",
		Content:    "one edited\n",
		Encoding:   read.Encoding,
		BaseSHA256: read.SHA256,
	})
	if !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("err = %v want conflict", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "a.txt"))
	testutil.FailErr(t, "read back", err)
	if string(onDisk) != "two\n" {
		t.Fatalf("conflicting save clobbered the file: %q", onDisk)
	}
}

func TestWriteProjectSourceJailAndValidation(t *testing.T) {
	t.Parallel()
	p, _ := writeTestProject(t, map[string]string{"a.txt": "one\n"})
	base := textfile.SHA256([]byte("one\n"))

	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "", Content: "x", BaseSHA256: base}); !errors.Is(err, ErrSourcePathInvalid) {
		t.Fatalf("empty path err = %v", err)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "a.txt", Content: "x", BaseSHA256: ""}); !errors.Is(err, ErrSourcePathInvalid) {
		t.Fatalf("empty base err = %v", err)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "../escape.txt", Content: "x", Encoding: textfile.UTF8, BaseSHA256: base}); !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("escape err = %v", err)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "missing.txt", Content: "x", Encoding: textfile.UTF8, BaseSHA256: base}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "a.txt", Content: "bin\x00ary", Encoding: textfile.UTF8, BaseSHA256: base}); !errors.Is(err, ErrSourceBinary) {
		t.Fatalf("binary err = %v", err)
	}
	big := strings.Repeat("x", int(SourceWriteMaxBytes)+1)
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "a.txt", Content: big, Encoding: textfile.UTF8, BaseSHA256: base}); !errors.Is(err, ErrSourceWriteTooLarge) {
		t.Fatalf("too large err = %v", err)
	}
	if _, err := writeProjectSource(p, SourceWriteRequest{Path: "a.txt", Content: "x", BaseSHA256: base}); !errors.Is(err, ErrSourceEncodingInvalid) {
		t.Fatalf("empty encoding err = %v", err)
	}
	if _, err := writeProjectSource(nil, SourceWriteRequest{Path: "a.txt", Content: "x", BaseSHA256: base}); !errors.Is(err, ErrSourceNoRoot) {
		t.Fatalf("nil project err = %v", err)
	}
}

func TestWriteProjectSourceOverReadCapIsConflict(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)
	big := strings.Repeat("y", int(SourceReadMaxBytes)+10)
	testutil.FailErr(t, "write big", os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644))

	_, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "big.txt",
		Content:    "small\n",
		Encoding:   textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("anything")),
	})
	if !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("err = %v want conflict for over-cap target", err)
	}
}

func TestReadProjectSourceOverLimitHasNoSHA(t *testing.T) {
	t.Parallel()
	p, root := writeTestProject(t, nil)
	big := strings.Repeat("z", int(SourceReadMaxBytes)+10)
	testutil.FailErr(t, "write big", os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644))

	got, err := ReadProjectSource(p, SourceReadRequest{Path: "big.txt"})
	testutil.FailErr(t, "read", err)
	if !got.OverLimit {
		t.Fatal("expected over_limit read")
	}
	if got.SHA256 != "" || got.Content != "" {
		t.Fatalf("over-limit sha=%q contentLen=%d", got.SHA256, len(got.Content))
	}
}

func TestWriteProjectSourceRootIDPinsAmbiguousPath(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	testutil.FailErr(t, "write A", os.WriteFile(filepath.Join(rootA, "README.md"), []byte("primary"), 0o644))
	testutil.FailErr(t, "write B", os.WriteFile(filepath.Join(rootB, "README.md"), []byte("secondary"), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{
			{ID: "ra", Path: rootA, Label: "a", IsPrimary: true},
			{ID: "rb", Path: rootB, Label: "b"},
		},
	}

	res, err := writeProjectSource(p, SourceWriteRequest{
		Path:       "README.md",
		RootID:     "rb",
		Content:    "updated",
		Encoding:   textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("secondary")),
	})
	testutil.FailErr(t, "pinned write", err)
	if res.Path != "README.md" {
		t.Fatalf("path = %q", res.Path)
	}
	gotB, err := os.ReadFile(filepath.Join(rootB, "README.md"))
	testutil.FailErr(t, "read back B", err)
	if string(gotB) != "updated" {
		t.Fatalf("secondary root content = %q, want updated", gotB)
	}
	gotA, err := os.ReadFile(filepath.Join(rootA, "README.md"))
	testutil.FailErr(t, "read back A", err)
	if string(gotA) != "primary" {
		t.Fatalf("primary root content = %q, want untouched", gotA)
	}
}

type feedCap struct {
	mu  sync.Mutex
	evs []api.SourceChange
}

func (c *feedCap) SourceChanged(_ context.Context, ev api.SourceChangesEvent) error {
	c.mu.Lock()
	c.evs = append(c.evs, ev.Changes...)
	c.mu.Unlock()
	return nil
}

func (c *feedCap) SourceChangedTx(ctx context.Context, _ *sql.Tx, ev api.SourceChangesEvent) error {
	return c.SourceChanged(ctx, ev)
}

func (c *feedCap) Deliver() {}

func (c *feedCap) events() []api.SourceChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]api.SourceChange, len(c.evs))
	copy(out, c.evs)
	return out
}

func TestWriteProjectSourceStampsChatAffiliation(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644))
	cap := &feedCap{}
	t.Cleanup(sourcefeed.Bind(cap))
	_, err := service.Write(t.Context(), "11111111-1111-4111-8111-111111111111", p, SourceWriteRequest{
		Path: "a.txt", Content: "hello world\n", Encoding: textfile.UTF8,
		BaseSHA256: textfile.SHA256([]byte("hello\n")), SessionID: "sess-1", Turn: 4,
	})
	testutil.FailErr(t, "write", err)
	walk, err := service.ledger.QueryWalk(t.Context(), p.ID, sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "sess-1"}, 20, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query ledger", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("walk effects = %+v", walk.Files)
	}
	effect := walk.Files[0].Effects[0]
	if effect.SessionID != "sess-1" || effect.Turn != 4 || effect.Origin != api.SourceChangeOriginUser {
		t.Fatalf("ledger affiliation = %+v", effect)
	}
	evs := cap.events()
	if len(evs) != 1 {
		t.Fatalf("emit count = %d", len(evs))
	}
	if evs[0].SessionID != "sess-1" || evs[0].Turn != 4 {
		t.Fatalf("emit affiliation = session=%q turn=%d", evs[0].SessionID, evs[0].Turn)
	}
}
