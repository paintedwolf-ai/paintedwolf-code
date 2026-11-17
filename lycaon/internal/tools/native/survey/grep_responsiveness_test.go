package survey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGrepReadSlotsAreCancelable(t *testing.T) {
	for range cap(grepReadSlots) {
		grepReadSlots <- struct{}{}
	}
	defer func() {
		for range cap(grepReadSlots) {
			<-grepReadSlots
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := readGrepFile(ctx, nil, "", nil)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued read error = %v", err)
	}
}

func TestGrepPerFileResultsAreBounded(t *testing.T) {
	re, err := compileGrepRegex("hit", false)
	testutil.FailErr(t, "compile pattern", err)
	for _, contextLines := range []int{0, 2} {
		search := &grepSearch{ctx: t.Context(), re: re, offset: 4, maxMatches: 3, contextLines: contextLines}
		matches, err := search.collectMatches("generated.txt", grepFileIdentity{}, []byte(strings.Repeat("hit\n", 100000)))
		testutil.FailErr(t, "match dense content", err)
		if len(matches) != 7 {
			t.Fatalf("retained %d results for a seven-match window", len(matches))
		}
	}
}
