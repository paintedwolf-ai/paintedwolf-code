package presence

// revocation identifies a step-away boundary within this host process.
// Receipts keep its pointer, so even a delayed commit cannot reopen an unlock.
type revocation struct{ reason EndReason }

func (b *Broker) invalidate(reason EndReason) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.epoch.Store(&revocation{reason: reason})
	clear(b.challenges)
}
