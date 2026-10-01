package bedrock

import (
	"testing"

	guidancetestsetup "github.com/lycaon/lycaon/internal/testsetup/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMain(m *testing.M) { guidancetestsetup.Install(); testutil.VerifyNoLeaks(m, nil) }
