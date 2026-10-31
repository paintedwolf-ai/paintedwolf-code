package promptloop_test

// workersLoaded models a chat whose turn ledger already loaded the worker
// tools and retired read schema, so dispatch and batch tests exercise them on
// loadable coordinator surfaces.
func workersLoaded(string) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{"task", "pack_board", "worker_cancel", "answer_decision", "extend_worker_budget", "command", "edit", "read"} {
		out[name] = true
	}
	return out
}
