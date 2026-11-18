package security

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/scanworker"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/wiring"
)

// TestMain also serves scanner subprocess requests.
func TestMain(m *testing.M) {
	gittestsetup.Enable()
	for _, arg := range os.Args[1:] {
		if arg == "internal-scan-worker" {
			if err := scanworker.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	testutil.SelectRaceTests(
		"TestChatStreamingTwoChannelE2E",
		"TestSSETopicInvalidationE2E",
		"TestCommandApprovalWildcardAskSSEMetadataE2E",
	)
	os.Exit(wiring.RunReleasingHosts(m))
}
