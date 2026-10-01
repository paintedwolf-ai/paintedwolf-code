package promptloop_test

// workersLoaded models a chat whose turn ledger already loaded the worker
// tools, so dispatch tests exercise task on the investigate surface the way a
// coordinator that predicted or requested delegation would see it.
func workersLoaded(string) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{"task", "pack_board", "worker_cancel", "answer_decision", "extend_worker_budget"} {
		out[name] = true
	}
	return out
}
