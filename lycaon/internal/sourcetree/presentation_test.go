package sourcetree

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func captureForTest(t *testing.T, v *View) *Presentation {
	t.Helper()
	var result *Presentation
	var err error
	testutil.WaitFor(t, 10*time.Second, func() bool {
		result, err = v.Capture(t.Context())
		return !errors.Is(err, pagedview.ErrPreparing) && !errors.Is(err, pagedview.ErrRevision)
	})
	testutil.FailErr(t, "capture presentation", err)
	t.Cleanup(result.Close)
	return result
}

func frameForTest(t *testing.T, v *View, ctx context.Context, request FrameRequest) (Frame, error) {
	t.Helper()
	presentation := captureForTest(t, v)
	return presentation.Frame(ctx, request)
}
func locateForTest(t *testing.T, v *View, ctx context.Context, address Address) (Location, pagedview.Revision, error) {
	return captureForTest(t, v).Locate(ctx, address)
}
func findForTest(t *testing.T, v *View, ctx context.Context, basis, query string, offset int64, limit int, sensitive bool) (SearchPage, error) {
	return captureForTest(t, v).Find(ctx, basis, query, offset, limit, sensitive)
}
