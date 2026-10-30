package commandprogram

import (
	"context"
	"errors"
	"strings"
)

type predicateQuota struct {
	ctx           context.Context
	port          PredicateMemory
	maximum, used int64
	releases      []func()
}

func (r PredicateRuntime) withQuota(ctx context.Context) (PredicateRuntime, error) {
	if r.Memory == nil && !r.LocalBoundsOnly {
		return r, errors.New("predicate runtime requires aggregate memory admission")
	}
	var release func()
	if r.Memory != nil {
		var err error
		release, err = r.Memory.Reserve(ctx, 512)
		if err == nil && release == nil {
			err = errors.New("predicate memory admission returned no lease")
		}
		if err != nil {
			return r, err
		}
	}
	r.quota = &predicateQuota{ctx: ctx, port: r.Memory, maximum: predicateBounds().memory, used: 512}
	if release != nil {
		r.quota.releases = []func(){release}
	}
	return r, nil
}
func (q *predicateQuota) alloc(n int64) error {
	if err := q.ctx.Err(); err != nil {
		return err
	}
	n += 32
	if n < 0 || n > q.maximum-q.used {
		return &PredicateLimitError{"evaluation_memory_bytes", q.maximum, q.used + n}
	}
	if q.port != nil {
		release, err := q.port.Reserve(q.ctx, n)
		if err == nil && release == nil {
			err = errors.New("predicate memory admission returned no lease")
		}
		if err != nil {
			return err
		}
		q.releases = append(q.releases, release)
	}
	q.used += n
	return nil
}
func (q *predicateQuota) close() {
	for i := len(q.releases) - 1; i >= 0; i-- {
		q.releases[i]()
	}
}
func (r PredicateRuntime) text(value PredicateValue) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	if len(value) == 1 {
		return value[0].Value, nil
	}
	size := 0
	for _, fragment := range value {
		size += len(fragment.Value)
	}
	if err := r.quota.alloc(int64(size)); err != nil {
		return "", err
	}
	var text strings.Builder
	text.Grow(size)
	for _, fragment := range value {
		text.WriteString(fragment.Value)
	}
	return text.String(), nil
}
