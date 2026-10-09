package sourceview

import (
	"bytes"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/zstdcodec"
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
