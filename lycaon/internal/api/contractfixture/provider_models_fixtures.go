package contractfixture

import (
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
)

func WithTestUserNotices(t *testing.T) TestDeps {
	t.Helper()
	notices := TestUserNotices(t)
	return func(d *hostapi.Dependencies) { d.Core.UserNotices = notices }
}
