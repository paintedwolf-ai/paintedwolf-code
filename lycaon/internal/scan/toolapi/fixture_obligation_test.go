package toolapi_test

import (
	"context"
)

type staticHead struct{ sha string }

func (s staticHead) HeadSHA(context.Context, string) (string, error) { return s.sha, nil }
