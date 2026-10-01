// Package gitlease serializes repository reads and writes within this process.
// External writers are outside the lease.
// Lock order is source repository before destination.
package gitlease

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
)

type lease struct {
	token chan struct{}
	refs  int
}

var leases = struct {
	sync.Mutex
	byKey map[string]*lease
}{byKey: make(map[string]*lease)}

// Repository keys leases by the Git common directory, shared by linked worktrees.
func Repository(ctx context.Context, dir string) (func(), error) {
	key, err := repositoryKey(ctx, dir)
	if err != nil {
		return nil, err
	}
	return acquire(ctx, key)
}

// Path leases a destination before it becomes a repository.
func Path(ctx context.Context, path string) (func(), error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve lease path: %w", err)
	}
	return acquire(ctx, filepath.Clean(key))
}

// CloneSource leases local source repositories; remote sources need no local lease.
func CloneSource(ctx context.Context, source string) (func(), error) {
	path, ok := gitargv.LocalCloneSourcePath(source)
	if !ok {
		return func() {}, nil
	}
	key, err := repositoryKey(ctx, path)
	if err != nil {
		// Cancellation stops the operation; other lookup failures are reported by clone.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return func() {}, nil
	}
	return acquire(ctx, key)
}

// acquire expects a canonical repository or destination key.
func acquire(ctx context.Context, key string) (func(), error) {
	leases.Lock()
	held := leases.byKey[key]
	if held == nil {
		held = &lease{token: make(chan struct{}, 1)}
		held.token <- struct{}{}
		leases.byKey[key] = held
	}
	held.refs++
	leases.Unlock()

	select {
	case <-ctx.Done():
		drop(key, held)
		return nil, ctx.Err()
	case <-held.token:
		var once sync.Once
		return func() {
			once.Do(func() {
				held.token <- struct{}{}
				drop(key, held)
			})
		}, nil
	}
}

func drop(key string, held *lease) {
	leases.Lock()
	defer leases.Unlock()
	held.refs--
	if held.refs == 0 && leases.byKey[key] == held {
		delete(leases.byKey, key)
	}
}

func repositoryKey(ctx context.Context, dir string) (string, error) {
	out, code, err := gitexec.Run(ctx, dir, []string{"rev-parse", "--git-common-dir"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("resolve git common directory: %s", strings.TrimSpace(string(out)))
	}
	common := strings.TrimSuffix(string(out), "\n")
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return "", fmt.Errorf("resolve git common directory: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(common); resolveErr == nil {
		common = resolved
	}
	return filepath.Clean(common), nil
}
