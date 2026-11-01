package bialy_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClientReapsAnEngineWithAnUnusableResponsePipe(t *testing.T) {
	for _, fault := range []string{"exec 1>&-", "printf 'invalid json\\n'"} {
		t.Run(fault, func(t *testing.T) {
			cfg := fakeConfig(t)
			body := strings.Replace(fakeEngine, `  case "$line" in`, `  case "$line" in
    *'"task":"break-response"'*)
      `+fault+`
      while IFS= read -r ignored; do :; done
      exit 0 ;;
  esac
  case "$line" in`, 1)
			testutil.FailErr(t, "install response fault engine", os.WriteFile(cfg.Binary, []byte(body), 0o755))
			client := bialy.New(cfg)
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			testutil.FailErr(t, "warm fault engine", client.Warm(ctx))
			_, _, err := client.Rank(ctx, decide.HeadCodeRank, "break-response", []string{"a", "b"})
			if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, decide.ErrDeadline) {
				t.Fatalf("response pipe fault = %v, want engine failure", err)
			}
			scores, _, err := client.Rank(ctx, decide.HeadCodeRank, "after", []string{"a", "b"})
			testutil.FailErr(t, "rank through replacement engine", err)
			if len(scores) != 2 || scores[1] != 3.7 {
				t.Fatalf("replacement scores = %v", scores)
			}
		})
	}
}
