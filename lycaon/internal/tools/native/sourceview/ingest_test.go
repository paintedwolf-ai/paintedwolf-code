package sourceview

import (
	"bytes"
	"errors"
	"github.com/lycaon/lycaon/internal/projectroot"
	attachmentformat "github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"syscall"
)

func TestContentIngestionEnforcesObservedBytes(t *testing.T) {
	for _, tc := range []struct {
		access Access
		code   string
	}{
		{AccessRead, "READ_FILE_TOO_LARGE"},
		{AccessMutate, "EDIT_FILE_TOO_LARGE"},
	} {
		maxBytes := tc.access.MaxBytes()
		for _, compressed := range []bool{false, true} {
			for _, extra := range []int{0, 1} {
				plain := strings.Repeat("x", int(maxBytes)+extra)
				var out []byte
				var err error
				if compressed {
					encoded, encodeErr := zstdcodec.Compress(strings.NewReader(plain))
					testutil.FailErr(t, "compress test input", encodeErr)
					out, err = readCompressedContentCapped("read", tc.access, bytes.NewReader(encoded), "output.txt", maxBytes)
				} else {
					out, err = readPlainContentCapped("read", tc.access, strings.NewReader(plain), "output.txt", maxBytes)
				}
				if extra == 0 {
					testutil.FailErr(t, "read input at cap", err)
					if string(out) != plain {
						t.Fatal("accepted content changed")
					}
					continue
				}
				var reject *toolrejection.ToolReject
				if out != nil || !errors.As(err, &reject) || reject.Code != tc.code {
					t.Fatalf("%s compressed=%v: partial/oversized content accepted: len=%d err=%v", tc.code, compressed, len(out), err)
				}
				if reject.Data["max_file_bytes"] != maxBytes || reject.Data["path"] != "output.txt" {
					t.Fatalf("limit feedback lost measured context: %#v", reject.Data)
				}
			}
		}
	}
}

// Each access has exactly one budget and one size code.
func TestAccessBudgets(t *testing.T) {
	if AccessRead.MaxBytes() != readcaps.MaxFileBytes || AccessMutate.MaxBytes() != readcaps.MaxMutationBytes {
		t.Fatalf("budgets = %d/%d", AccessRead.MaxBytes(), AccessMutate.MaxBytes())
	}
	reject := toolrejection.AsToolReject(MutationSizeReject("edit", "a.txt", 9))
	if reject == nil || reject.Code != "EDIT_FILE_TOO_LARGE" || reject.Data["size"] != int64(9) || reject.Data["max_file_bytes"] != int64(readcaps.MaxMutationBytes) {
		t.Fatalf("mutation reject = %#v", reject)
	}
	if _, measured := toolrejection.AsToolReject(AccessRead.SizeReject("read", "a.txt", 0, 1)).Data["size"]; measured {
		t.Fatal("an unmeasured size must be omitted, not reported as zero")
	}
}

func TestBinaryReadRecoveryUsesWorkspaceViewerFormats(t *testing.T) {
	root := t.TempDir()
	exts := append(attachmentformat.WorkspaceImageExtensions(), ".PNG", ".ico", ".bmp", ".tiff", ".txt", "")
	for _, ext := range exts {
		name := "asset" + ext
		testutil.FailErr(t, "write binary fixture", os.WriteFile(filepath.Join(root, name), []byte{0, 1, 2, 0xff}, 0600))
		resolved := projectpaths.Resolved{Root: projectroot.RootRef{Path: root}, ScopeRel: name, DisplayPath: name}
		_, err := ReadTextDocument("read", AccessRead, resolved, 0)
		reject := toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != "READ_BINARY_DENIED" {
			t.Fatalf("%s reject = %v", name, err)
		}
		_, want := attachmentformat.WorkspaceImageMIME(ext)
		if reject.Data["workspace_image_supported"] != want {
			t.Fatalf("%s viewer eligibility = %v, want %v", name, reject.Data, want)
		}
	}
}

func TestReadContentCappedFromRootCoversSmallLargeAndGrowingInputs(t *testing.T) {
	root := t.TempDir()
	small := "small-body"
	large := strings.Repeat("L", (64<<10)+128)
	testutil.FailErr(t, "write small", os.WriteFile(filepath.Join(root, "small.txt"), []byte(small), 0600))
	testutil.FailErr(t, "write large", os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0600))

	readRoot, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open read root", err)
	defer func() { _ = readRoot.Close() }()

	gotSmall, err := ReadContentCappedFromRoot("read", AccessRead, readRoot, fseffect.Location{Root: root, Rel: "small.txt"}, "small.txt", false, 1024)
	testutil.FailErr(t, "read small from held root", err)
	if string(gotSmall) != small {
		t.Fatalf("small body = %q, want %q", gotSmall, small)
	}

	gotLarge, err := ReadContentCapped("read", AccessRead, fseffect.Location{Root: root, Rel: "large.txt"}, "large.txt", false, int64(len(large)+10))
	testutil.FailErr(t, "read large", err)
	if string(gotLarge) != large {
		t.Fatalf("large body len = %d, want %d", len(gotLarge), len(large))
	}

	if _, err := ReadContentCappedFromRoot("read", AccessRead, readRoot, fseffect.Location{Root: root, Rel: "large.txt"}, "large.txt", false, 16); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("expected size rejection for oversized file, got %v", err)
	}

	fifoOK := filepath.Join(root, "stream-ok.fifo")
	if err := syscall.Mkfifo(fifoOK, 0600); err == nil {
		rHold, err := os.OpenFile(fifoOK, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		testutil.FailErr(t, "open fifo reader hold", err)
		defer func() { _ = rHold.Close() }()
		w, err := os.OpenFile(fifoOK, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		testutil.FailErr(t, "open fifo writer", err)
		_, err = w.WriteString("hello-fifo")
		testutil.FailErr(t, "write fifo", err)
		testutil.FailErr(t, "close fifo writer", w.Close())

		gotFIFO, err := ReadContentCappedFromRoot("read", AccessRead, readRoot, fseffect.Location{Root: root, Rel: "stream-ok.fifo"}, "stream-ok.fifo", false, 64)
		testutil.FailErr(t, "read fifo within cap", err)
		if string(gotFIFO) != "hello-fifo" {
			t.Fatalf("fifo body = %q, want hello-fifo", gotFIFO)
		}
	}

	fifoBig := filepath.Join(root, "stream-big.fifo")
	if err := syscall.Mkfifo(fifoBig, 0600); err == nil {
		rHold, err := os.OpenFile(fifoBig, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		testutil.FailErr(t, "open big fifo reader hold", err)
		defer func() { _ = rHold.Close() }()
		w, err := os.OpenFile(fifoBig, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		testutil.FailErr(t, "open big fifo writer", err)
		_, err = w.WriteString("0123456789abcdef")
		testutil.FailErr(t, "write big fifo", err)
		testutil.FailErr(t, "close big fifo writer", w.Close())

		_, err = ReadContentCappedFromRoot("read", AccessRead, readRoot, fseffect.Location{Root: root, Rel: "stream-big.fifo"}, "stream-big.fifo", false, 5)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "READ_FILE_TOO_LARGE" {
			t.Fatalf("oversized fifo reject = %v", err)
		}
	}
}
