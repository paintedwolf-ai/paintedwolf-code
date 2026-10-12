package loopguard

import "context"

func (g *MemoryDoomLoopGuard) SetReviewIntentResolver(resolve func(context.Context, string) (string, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reviewIntent = resolve
}

// Fresh intent resets repetition before the next invocation is checked.
func (g *MemoryDoomLoopGuard) refreshReviewIntent(ctx context.Context, sessionID string) error {
	g.mu.RLock()
	resolve := g.reviewIntent
	g.mu.RUnlock()
	if resolve == nil {
		return nil
	}
	intent, err := resolve(ctx, sessionID)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	previous, known := g.intents.Load(sessionID)
	if !known || previous != intent {
		g.states.Delete(sessionID)
		g.fruitless.Delete(sessionID)
		g.codeRuns.Delete(sessionID)
		g.effects.Delete(sessionID)
		g.intents.Store(sessionID, intent)
	}
	return nil
}
