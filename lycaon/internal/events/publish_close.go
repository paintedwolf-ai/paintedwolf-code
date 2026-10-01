package events

import "context"

// Close cancels pending projections and drains active deliveries within ctx's deadline.
func (p *Publisher) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.closed.Store(true)
	p.attentionMu.Lock()
	if p.attentionTimer != nil {
		if p.attentionTimer.Stop() {
			p.attentionWG.Done()
		}
		p.attentionTimer = nil
	}
	p.attentionMu.Unlock()
	p.messagePatchesMu.Lock()
	patches := p.messagePatches
	if patches != nil {
		patches.stop()
	}
	p.messagePatchesMu.Unlock()
	done := make(chan struct{})
	go func() {
		p.attentionWG.Wait()
		if patches != nil {
			patches.wg.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
