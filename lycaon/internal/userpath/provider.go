package userpath

import (
	"context"
	"io/fs"
	"os"
	osexec "os/exec"
	"sync"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// Provider resolves one immutable process PATH.
type Provider struct {
	cfg  Config
	once sync.Once
	snap Snapshot

	// Tests replace host dependencies through these seams.
	lookupEnv    func(string) (string, bool)
	environ      func() []string
	stat         func(string) (fs.FileInfo, error)
	run          func(context.Context, *osexec.Cmd) error
	prepare      func(context.Context, string, []string, execpkg.ExecOpts) (*osexec.Cmd, func(), error)
	accountShell func(context.Context) (string, error)
}

// NewProvider builds a provider over the given catalog.
func NewProvider(cfg Config) *Provider {
	return &Provider{
		cfg:          cfg,
		lookupEnv:    os.LookupEnv,
		environ:      os.Environ,
		stat:         defaultStat,
		run:          defaultRun,
		prepare:      execpkg.PrepareCommand,
		accountShell: defaultAccountShell,
	}
}

// Resolve returns the process-lifetime snapshot, probing on first call only.
func (p *Provider) Resolve(ctx context.Context) Snapshot {
	p.once.Do(func() { p.snap = p.resolve(ctx) })
	return p.snap
}

func (p *Provider) resolve(ctx context.Context) Snapshot {
	raw, err := p.probeShell(ctx)
	if err == nil {
		if entries := parseEntries(raw, p.cfg.Probe.MaxEntries); len(entries) > 0 {
			return Snapshot{entries: entries, source: SourceProbe}
		}
		err = errEmptyProbeResult
	}

	// Preserve the launch environment before using the catalog fallback.
	inherited, _ := p.lookupEnv("PATH")
	if entries := parseEntries(inherited, p.cfg.Probe.MaxEntries); len(entries) > 0 {
		return Snapshot{
			entries: entries,
			source:  SourceInherited,
			failure: classifyProbeFailure(err),
			reason:  err.Error(),
		}
	}

	return Snapshot{
		entries: parseEntries(joinList(p.cfg.Fallback), p.cfg.Probe.MaxEntries),
		source:  SourceFallback,
		failure: classifyProbeFailure(err),
		reason:  err.Error(),
	}
}
