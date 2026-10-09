package worker

import (
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type subjectQueue interface {
	Get(string) (*api.WorkerTask, bool)
}

func captureWorkerSubject(tctx tools.ToolContext, queue subjectQueue, id string) {
	if queue == nil {
		return
	}
	task, ok := queue.Get(strings.TrimSpace(id))
	if ok && task != nil && strings.TrimSpace(task.ParentSessionID) == strings.TrimSpace(tctx.Identity.SessionID) {
		tctx.SetDisplaySubject(task.Brief)
	}
}
