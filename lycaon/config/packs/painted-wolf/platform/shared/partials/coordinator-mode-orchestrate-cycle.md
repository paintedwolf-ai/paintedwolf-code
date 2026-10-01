## Coordinator cycle

**`wait()`** parks the agent; the first matching **`conditions`** entry resumes it, and `timeout_ms` is always the deadline backstop.

| Trigger | Fires when |
|---------|------------|
| `next_worker_done` | One `task()` job finishes |
| `all_workers_idle` | Every in-flight job terminal |
| `overlay_promote_pending` | Write overlay awaits promote/reject |
| `scan_done` | Host security scan terminal |

| Situation | End turn with |
|-----------|---------------|
| User/synthesis idle | Prose — no `wait()` |
| Workers are in flight; no independent dispatch, decision, or integration is ready | **`wait(timeout_ms=120000–300000, conditions=[{kind: next_worker_done}])`** — resume on the next completion |
| Fan-out you will only act on as a set | use `{kind: all_workers_idle}` and raise the timeout (900000–1800000 ms) |
| Host — idle, not synthesis | **`wait(resume=true)`** — no prose |
| Host — dispatch/park | Tool call (`Code: COORDINATOR_HOST_TURN_REQUIRES_WAIT`) |
| After `answer_decision`, `extend_worker_budget`, or `decline_worker_budget`, when no other work is ready | **`wait(resume=true)`** — resume the interrupted wait |

After dispatch, inspect receipts, repair rejected calls, and start remaining ready work within capacity. Resolve worker decisions and integrate available results. Use `wait` when the next useful action depends on a worker result.

Prefer **`wait(resume=true)`** when the only expected wake is the worker you just answered or extended — a new lease is for a new wait, not decision/budget resume.
