package sessions

import "context"

// begin serializes evidence transitions while allowing waiting providers,
// tools, and observers to cancel. Every admitted operation joins shutdown.
func (i *credentialIndex) begin(ctx context.Context) (context.Context, func(), error) {
	ctx, finish, err := i.work.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	release, err := i.access.Acquire(ctx, "evidence")
	if err != nil {
		finish()
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		finish()
		return nil, nil, err
	}
	return ctx, func() {
		release()
		finish()
	}, nil
}
