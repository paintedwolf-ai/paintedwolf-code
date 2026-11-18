package nfsdarwin

// SetConfigCallbacksForTest updates the fault-injection callbacks for client file sync and runtime directory cleanup.
func (a *Adapter) SetConfigCallbacksForTest(clientSyncFn func(string) error, removeAllFn func(string) error) {
	a.mu.Lock()
	a.cfg.ClientSyncFn = clientSyncFn
	a.cfg.RemoveAllFn = removeAllFn
	a.mu.Unlock()
}
