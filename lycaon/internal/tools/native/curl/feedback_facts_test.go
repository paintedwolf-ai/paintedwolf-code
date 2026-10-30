package curl

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCurlLimitFeedbackCarriesByteUnits(t *testing.T) {
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(bytes.Repeat([]byte("a"), 128)); err != nil {
		testutil.FailErr(t, "compress response fixture", err)
	}
	if err := z.Close(); err != nil {
		testutil.FailErr(t, "finish compressed fixture", err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	for _, mode := range []string{"compressed", "upload"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			h.owners.limits["stream_bytes"] = 64
			args := []string{"--compressed", server.URL}
			before := requests.Load()
			if mode == "upload" {
				h.write("large.txt", strings.Repeat("x", 65))
				args = []string{"--data-binary", "@large.txt", server.URL}
			}
			result := h.curl(t.Context(), args...)
			reject := toolrejection.AsToolReject(result.err)
			if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["resource_unit"] != "bytes" || reject.Data["maximum"] != int64(64) || reject.Data["observed"] != int64(65) || result.stdout != "" {
				t.Fatalf("feedback result=%+v reject=%+v", result.result, reject)
			}
			if mode == "upload" && requests.Load() != before {
				t.Fatal("oversized upload reached transport")
			}
		})
	}
}

func TestCurlUsageFeedbackCarriesActualReason(t *testing.T) {
	h := newHarness(t)
	result := h.curl(t.Context(), "--max-time", "-1", "https://example.invalid")
	reject := toolrejection.AsToolReject(result.err)
	if reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" || reject.Data["reason"] == "" || reject.Data["reason"] != reject.Data["expected"] {
		t.Fatalf("feedback %+v error=%v", reject, result.err)
	}
}
