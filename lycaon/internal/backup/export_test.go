package backup

import "context"

func withGitClosureStats(ctx context.Context) (context.Context, *gitClosureStats, func()) {
	stats := &gitClosureStats{}
	s := newGitClosureSession(stats)
	return context.WithValue(ctx, gitClosureSessionKey{}, s), stats, s.close
}
