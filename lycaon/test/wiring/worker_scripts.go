package wiring

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

type QueuedWorkerScripts struct {
	mu      sync.Mutex
	queues  map[string][][]api.ToolCall
	active  map[string][]api.ToolCall
	step    map[string]int
	results map[string]string
}

func NewQueuedWorkerScripts() *QueuedWorkerScripts {
	return &QueuedWorkerScripts{
		queues:  map[string][][]api.ToolCall{},
		active:  map[string][]api.ToolCall{},
		step:    map[string]int{},
		results: map[string]string{},
	}
}

func (q *QueuedWorkerScripts) Push(agentType string, script ...api.ToolCall) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queues[agentType] = append(q.queues[agentType], script)
}

func (q *QueuedWorkerScripts) Mock(h **Harness) *llm.KickDrivenMock {
	return llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(ctx context.Context, _ llm.Snapshot) modelcall.Completion {
			jobID := workercontext.Job(ctx)
			if jobID == "" {
				q.mu.Lock()
				defer q.mu.Unlock()
				if list := q.queues["coordinator"]; len(list) > 0 {
					script := list[0]
					idx := q.step["coordinator"]
					if idx < len(script) {
						q.step["coordinator"] = idx + 1
						return modelcall.Completion{ToolCalls: []api.ToolCall{script[idx]}}
					}
					q.queues["coordinator"] = list[1:]
					q.step["coordinator"] = 0
					if reply := q.results["coordinator"]; reply != "" {
						return modelcall.Completion{Content: reply}
					}
				}
				return modelcall.Completion{Content: "All obligations are complete."}
			}
			task, ok := (*h).Workers.Queue.Get(jobID)
			if !ok || task == nil {
				return modelcall.Completion{Content: "Done."}
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			idx, hasStarted := q.step[jobID]
			if !hasStarted {
				q.step[jobID] = 0
				if list := q.queues[task.AgentType]; len(list) > 0 {
					q.active[jobID] = list[0]
					q.queues[task.AgentType] = list[1:]
				}
			}
			script := q.active[jobID]
			if idx >= len(script) {
				return modelcall.Completion{Content: "Done."}
			}
			call := script[idx]
			q.step[jobID] = idx + 1
			return modelcall.Completion{ToolCalls: []api.ToolCall{call}}
		},
	})
}
