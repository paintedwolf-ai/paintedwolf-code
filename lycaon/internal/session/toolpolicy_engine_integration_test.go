//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolpolicyEngineListInvokeParityEmptyArgs(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	for _, meta := range listed {
		if err := fix.Mgr.Guards.Policy().EvaluateInvoke(ctx, fix.Sess, meta.Name, nil); err != nil {
			t.Fatalf("listed tool %q failed empty-args invoke: %v", meta.Name, err)
		}
	}
}
