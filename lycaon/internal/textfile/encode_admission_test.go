package textfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEncodeRejectsBeforeAllocatingOutput(t *testing.T) {
	text := strings.Repeat("a😀é", 4096)
	for _, encoding := range []string{UTF8, UTF8BOM, UTF16LE, UTF16LEBOM, UTF16BE, UTF16BEBOM} {
		size, err := EncodedSize(text, encoding)
		testutil.FailErr(t, "EncodedSize failed", err)
		var failure error
		allocations := testing.AllocsPerRun(1, func() {
			_, failure = EncodeBounded(text, encoding, Limits{MaxRawBytes: size - 1, MaxTextBytes: int64(len(text))})
		})
		if !errors.Is(failure, ErrRawTooLarge) || allocations != 0 {
			t.Fatalf("%s refusal %v allocated %v", encoding, failure, allocations)
		}
		encoded, err := EncodeBounded(text, encoding, Limits{MaxRawBytes: size, MaxTextBytes: int64(len(text))})
		if err != nil || int64(len(encoded)) != size {
			t.Fatalf("%s length %d want %d err %v", encoding, len(encoded), size, err)
		}
	}
}
