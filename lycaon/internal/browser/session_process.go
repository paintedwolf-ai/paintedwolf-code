package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// launchBrowserProcess keeps URL readiness separate from the native group owner.
// Both explicit unconfined policy and strict boundary use the same launch door;
// newHeadlessLauncher selects Chromium's own sandbox flags before this point.
func launchBrowserProcess(ctx context.Context, l *launcher.Launcher, chromePath string, boundary *confine.Confinement) (string, func(), error) {
	l = l.Delete(flags.Leakless)
	plan := execpkg.AgentLaunch(execpkg.LaunchManagedBrowser, "chrome", boundary).WithReducedEnvironment()
	plan.CurrentAuthority = func(context.Context) error { return ctx.Err() }
	cmd, cleanup, err := execpkg.PrepareCommand(context.WithoutCancel(ctx), chromePath, l.FormatArgs(), execpkg.ExecOpts{
		Launch: plan,
	})
	if err != nil {
		return "", nil, err
	}
	parser := launcher.NewURLParser().Context(ctx)
	cmd.Stdout, cmd.Stderr = parser, parser
	group, err := execpkg.StartInOwnGroup(cmd)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	exit := make(chan struct{})
	go func() { _ = group.Wait(); cleanup(); close(exit) }()
	stop := func() { group.Stop(); <-exit }
	url, err := waitParserURL(ctx, parser, exit)
	if err != nil {
		detail := trimBuf(parser.Err().Error())
		stop()
		return "", nil, fmt.Errorf("%w: %s", err, detail)
	}
	resolved, err := launcher.ResolveURL(url)
	if err != nil {
		stop()
		return "", nil, err
	}
	return resolved, stop, nil
}

func waitParserURL(ctx context.Context, parser *launcher.URLParser, exit <-chan struct{}) (string, error) {
	select {
	case url := <-parser.URL:
		return url, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-exit:
		return "", fmt.Errorf("browser exited before publishing debug URL")
	}
}
