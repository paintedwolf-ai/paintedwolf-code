package sourcefeed

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
)

func TestMain(m *testing.M) {
	release := repochangetest.Suite()
	code := m.Run()
	release()
	os.Exit(code)
}
