// Package prompttest supplies tool fixtures for prompt assembly tests.
package prompttest

import (
	"context"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func CoordinatorTools(_ context.Context, _ *api.Session, profileID string) ([]tools.ToolMeta, error) {
	if profileID != prompts.CoordinatorProfileID {
		return nil, nil
	}
	var metas []tools.ToolMeta
	for _, name := range []string{
		"task", "read", "grep", "write", "edit", "replace_lines", "pack_board",
		"state_start", "workflow_catalog_summaries",
	} {
		metas = append(metas, tools.ToolMeta{Name: name})
	}
	return metas, nil
}
