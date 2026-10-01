package git

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// Used only when no author is configured; scoped to one invocation.
const (
	defaultCommitAuthorName  = "Painted Wolf Code"
	defaultCommitAuthorEmail = "commits@paintedwolf.local"
)

// commitIdentity reads configured authorship, which the isolated execution
// profile would hide.
func commitIdentity(ctx context.Context, dir string) gitexec.Identity {
	if id, ok := gitexec.ResolveIdentity(ctx, dir); ok {
		return id
	}
	return gitexec.Identity{Name: defaultCommitAuthorName, Email: defaultCommitAuthorEmail}
}

func hermeticOpts(timeout time.Duration) gitexec.Opts {
	return gitexec.Opts{Profile: gitexec.ProfileHermetic, Timeout: timeout}
}

func networkOpts(remoteURL string, timeout time.Duration) gitexec.Opts {
	return gitexec.Opts{
		Profile:   gitexec.ProfileNetwork,
		RemoteURL: remoteURL,
		Timeout:   timeout,
	}
}
