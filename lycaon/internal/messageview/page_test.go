package messageview

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptPageByteBudgetKeepsTheRequestedEdge(t *testing.T) {
	rows := make([]api.Message, 100)
	for i := range rows {
		rows[i] = api.Message{Ord: int64(i + 1), Content: strings.Repeat("x", 16000)}
	}
	for _, forward := range []bool{false, true} {
		page, err := BoundTranscriptPage(api.SessionTranscriptPage{Messages: rows}, forward, func(ord int64) (string, error) {
			return strings.Repeat("c", int(ord)), nil
		})
		testutil.FailErr(t, "bound transcript page", err)
		if len(page.Messages) >= len(rows) || len(page.Messages) == 0 {
			t.Fatalf("window size=%d", len(page.Messages))
		}
		if forward {
			if page.Messages[0].Ord != 1 || page.AfterCursor == "" {
				t.Fatal("forward budget lost starting edge or continuation")
			}
		} else if page.Messages[len(page.Messages)-1].Ord != 100 || page.BeforeCursor == "" {
			t.Fatal("backward budget lost ending edge or continuation")
		}
	}
}
