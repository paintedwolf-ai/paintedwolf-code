package nativegen

import "context"

func (p *Runtime) acquire(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.lifetime, cancel)
	p.admissionMu.Lock()
	p.foreground++
	if p.preparationCancel != nil {
		p.preparationCancel()
	}
	p.admissionMu.Unlock()
	finish := func(acquired bool) {
		stop()
		cancel()
		p.admissionMu.Lock()
		p.foreground--
		if acquired {
			p.gate <- struct{}{}
		}
		p.admissionMu.Unlock()
	}
	select {
	case <-ctx.Done():
		finish(false)
		return nil, nil, ctx.Err()
	case <-p.gate:
		if p.lifetime.Err() != nil || ctx.Err() != nil {
			finish(true)
			return nil, nil, context.Canceled
		}
		return ctx, func() { finish(true) }, nil
	}
}

func (p *Runtime) acquirePreparation(ctx context.Context) (context.Context, func(), bool) {
	p.admissionMu.Lock()
	defer p.admissionMu.Unlock()
	if ctx.Err() != nil || p.lifetime.Err() != nil || p.foreground > 0 || p.preparationCancel != nil {
		return nil, nil, false
	}
	select {
	case <-p.gate:
	default:
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.lifetime, cancel)
	p.preparationCancel = cancel
	return ctx, func() {
		stop()
		cancel()
		p.admissionMu.Lock()
		p.preparationCancel = nil
		p.gate <- struct{}{}
		p.admissionMu.Unlock()
	}, true
}
