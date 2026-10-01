package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	guidancetestsetup.Install()
	anchortestsetup.Install()
	testutil.VerifyNoLeaks(m, func() {
		repochange.ResetWatchersForTest()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := sourcecatalog.Process().Drain(ctx); err != nil {
			panic(fmt.Errorf("drain source catalog: %w", err))
		}
	})
}
