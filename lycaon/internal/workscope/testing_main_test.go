package workscope

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestMain(m *testing.M) { testutil.VerifyNoLeaks(m, nil) }
