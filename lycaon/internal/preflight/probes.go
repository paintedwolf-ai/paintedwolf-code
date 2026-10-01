package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/dotversion"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/userpath"
)

// diskSpaceFloorBytes is the free-space level below which the app warns. Below
// this, scanner extraction, browser profiles, and the store all start failing in
// ways that look like unrelated bugs.
const diskSpaceFloorBytes = 2 << 30 // 2 GiB

// ---------------------------------------------------------------------------
// os_version
// ---------------------------------------------------------------------------

type osVersionProbe struct{}

func (osVersionProbe) ID() string { return "os_version" }

func (osVersionProbe) Run(_ context.Context, env Env) Result {
	if env.OSProductVer == nil {
		return Result{Status: StatusOK}
	}
	got, err := env.OSProductVer()
	if err != nil || strings.TrimSpace(got) == "" {
		// An unreadable sysctl is not evidence the OS is too old.
		return Result{Status: StatusDegraded, Code: CodeOSBelowFloor, Detail: map[string]string{
			"reason": ReasonOSUnreadable,
		}}
	}

	want := platformfloor.MacOSMin()
	cmp, err := dotversion.Compare(got, want)
	if err != nil {
		return Result{Status: StatusOK}
	}
	if cmp < 0 {
		// Stamped explicitly: the catalog resolves this code's tier by exact match
		// on `reason`.
		return Result{Status: StatusBlocked, Code: CodeOSBelowFloor, Detail: map[string]string{
			"reason": ReasonOSBelowFloor,
			"want":   want,
			"got":    got,
		}}
	}
	return Result{Status: StatusOK}
}

// ---------------------------------------------------------------------------
// config_dir
// ---------------------------------------------------------------------------

type configDirProbe struct{}

func (configDirProbe) ID() string { return "config_dir" }

func (configDirProbe) Run(_ context.Context, env Env) Result {
	if strings.TrimSpace(env.ConfigDir) == "" {
		return Result{Status: StatusBlocked, Code: CodeConfigDirUnwritable, Detail: map[string]string{
			"reason": "unresolved",
		}}
	}
	if err := os.MkdirAll(env.ConfigDir, 0o700); err != nil {
		if res, ok := hostFileLimitResult(err); ok {
			return res
		}
		return Result{Status: StatusBlocked, Code: CodeConfigDirUnwritable, Detail: map[string]string{
			"reason": "mkdir",
		}}
	}

	// Writability is proven by writing, not by reading a mode bit: an ACL or a
	// read-only mount does not show up in the permission bits.
	probeFile := filepath.Join(env.ConfigDir, ".preflight-write-check")
	if err := os.WriteFile(probeFile, []byte("ok"), 0o600); err != nil {
		if res, ok := hostFileLimitResult(err); ok {
			return res
		}
		return Result{Status: StatusBlocked, Code: CodeConfigDirUnwritable, Detail: map[string]string{
			"reason": "write",
		}}
	}
	if err := os.Remove(probeFile); err != nil {
		return Result{Status: StatusBlocked, Code: CodeConfigDirUnwritable, Detail: map[string]string{
			"reason": "remove",
		}}
	}
	return Result{Status: StatusOK}
}

// hostFileLimitResult reports the descriptor-exhaustion verdict when err is the
// engine (EMFILE) or the machine (ENFILE) out of descriptors. Every file-opening
// probe routes its error here first instead of blaming its own subject.
func hostFileLimitResult(err error) (Result, bool) {
	var reason string
	switch {
	case errors.Is(err, syscall.EMFILE):
		reason = ReasonFileLimitProcess
	case errors.Is(err, syscall.ENFILE):
		reason = ReasonFileLimitSystem
	default:
		return Result{}, false
	}
	return Result{Status: StatusBlocked, Code: CodeHostFileLimitReached, Detail: map[string]string{
		"reason": reason,
	}}, true
}

// ---------------------------------------------------------------------------
// disk_space
// ---------------------------------------------------------------------------

type diskSpaceProbe struct{}

func (diskSpaceProbe) ID() string { return "disk_space" }

func (diskSpaceProbe) Run(_ context.Context, env Env) Result {
	if env.FreeBytes == nil {
		return Result{Status: StatusOK}
	}
	free, err := env.FreeBytes(env.ConfigDir)
	if err != nil {
		// Never block on an unreadable statfs.
		return Result{Status: StatusOK}
	}
	if free < diskSpaceFloorBytes {
		return Result{Status: StatusDegraded, Code: CodeDiskSpaceLow, Detail: map[string]string{
			"free_mb": strconv.FormatUint(free/(1<<20), 10),
		}}
	}
	return Result{Status: StatusOK}
}

// ---------------------------------------------------------------------------
// command_path
// ---------------------------------------------------------------------------

type commandPathProbe struct{}

func (commandPathProbe) ID() string { return "command_path" }

func (commandPathProbe) Run(_ context.Context, env Env) Result {
	if env.UserPathSource == "" || env.UserPathSource == userpath.SourceProbe || env.UserPathSource == userpath.SourceConfigured {
		return Result{Status: StatusOK}
	}
	detail := map[string]string{"source": string(env.UserPathSource)}
	if env.UserPathFailure != userpath.FailureNone {
		detail["reason"] = string(env.UserPathFailure)
	}
	return Result{Status: StatusDegraded, Code: CodeCommandPathLimited, Detail: detail}
}

// ---------------------------------------------------------------------------
// git_engine
// ---------------------------------------------------------------------------

type gitEngineProbe struct{}

func (gitEngineProbe) ID() string { return "git_engine" }

// Run asserts the bundled git toolchain resolves and matches the pin. Green on
// a machine with no host git — the probe never consults PATH.
func (gitEngineProbe) Run(_ context.Context, env Env) Result {
	var err error
	if env.ResolveGitEngine != nil {
		err = env.ResolveGitEngine()
	} else {
		_, err = gitengine.BinaryPath()
	}
	if err != nil {
		detail := map[string]string{}
		var ue *gitengine.UnavailableError
		if errors.As(err, &ue) {
			detail["reason"] = ue.Reason
			if ue.Expected != "" {
				detail["expected"] = ue.Expected
			}
			if ue.Found != "" {
				detail["found"] = ue.Found
			}
		}
		return Result{Status: StatusDegraded, Code: CodeGitEngineUnavailable, Detail: detail}
	}
	return Result{Status: StatusOK}
}

// ---------------------------------------------------------------------------
// scanner_engine
// ---------------------------------------------------------------------------

type scannerEngineProbe struct{}

func (scannerEngineProbe) ID() string { return "scanner_engine" }

func (scannerEngineProbe) Run(_ context.Context, env Env) Result {
	if env.ResolveScanner == nil {
		return Result{Status: StatusOK}
	}
	_, reason, err := env.ResolveScanner()
	if err == nil {
		return Result{Status: StatusOK}
	}
	if res, ok := hostFileLimitResult(err); ok {
		return res
	}
	if reason == "" {
		reason = "not_found"
	}
	return Result{Status: StatusDegraded, Code: CodeScannerEngineUnavailable, Detail: map[string]string{
		"reason": reason,
	}}
}

// ---------------------------------------------------------------------------
// browser_engine
// ---------------------------------------------------------------------------

type browserEngineProbe struct{}

func (browserEngineProbe) ID() string { return "browser_engine" }

// Run exercises the same live launch/page/screenshot path as the visual tools.
func (browserEngineProbe) Run(ctx context.Context, env Env) Result {
	if env.CheckBrowser == nil {
		return Result{Status: StatusOK}
	}
	reason, err := env.CheckBrowser(ctx)
	if err == nil {
		return Result{Status: StatusOK}
	}
	if reason == "" {
		reason = ReasonBrowserUnusable
	}
	return Result{Status: StatusDegraded, Code: CodeBrowserEngineUnavailable, Detail: map[string]string{
		"reason": string(reason),
	}}
}

// ---------------------------------------------------------------------------
// decision_engine
// ---------------------------------------------------------------------------

type decisionEngineProbe struct{}

func (decisionEngineProbe) ID() string { return "decision_engine" }

// Run reports whether the local decision engine can answer. Without it every
// ranking site keeps its lexical order and every turn decision falls back, so
// the app works; the probe degrades rather than blocks.
func (decisionEngineProbe) Run(ctx context.Context, env Env) Result {
	if env.CheckDecisionEngine == nil {
		return Result{Status: StatusOK}
	}
	reason := env.CheckDecisionEngine(ctx)
	if reason == "" {
		return Result{Status: StatusOK}
	}
	return Result{Status: StatusDegraded, Code: CodeDecisionEngineUnavailable, Detail: map[string]string{
		"reason": string(reason),
	}}
}

// ---------------------------------------------------------------------------
// provider_configured
// ---------------------------------------------------------------------------

type providerConfiguredProbe struct{}

func (providerConfiguredProbe) ID() string { return "provider_configured" }

// Run reports whether the host can take a turn: every assigned role needs a
// configured provider, not merely some provider. An unwired callback degrades.
func (providerConfiguredProbe) Run(_ context.Context, env Env) Result {
	if env.ProviderCount == nil || env.MissingRoleProviders == nil {
		return Result{
			Status: StatusDegraded,
			Code:   CodeNoProviderConfigured,
			Detail: map[string]string{"reason": "probe_unwired"},
		}
	}
	if env.ProviderCount() == 0 {
		return Result{Status: StatusDegraded, Code: CodeNoProviderConfigured}
	}
	if missing := env.MissingRoleProviders(); len(missing) > 0 {
		return Result{Status: StatusDegraded, Code: CodeNoProviderConfigured, Detail: missing}
	}
	return Result{Status: StatusOK}
}

// ---------------------------------------------------------------------------
// lite_slot
// ---------------------------------------------------------------------------

type liteSlotProbe struct{}

func (liteSlotProbe) ID() string { return ProbeLiteSlot }

// Run reports observed lite-slot health; configuration belongs to
// provider_configured. An unwired callback is ok: nothing observed is not down.
func (liteSlotProbe) Run(_ context.Context, env Env) Result {
	if env.LiteSlotUnavailable == nil {
		return Result{Status: StatusOK}
	}
	down, detail := env.LiteSlotUnavailable()
	if !down {
		return Result{Status: StatusOK}
	}
	return Result{Status: StatusDegraded, Code: CodeLiteUnavailable, Detail: detail}
}

// RedactPath renders a path for user-facing preflight output without the raw
// $HOME prefix. It is the one redactor for everything this package emits,
// including the stop-screen report.
func RedactPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" || p == "." {
		return p
	}
	if p == string(filepath.Separator) {
		return "filesystem root"
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		home = filepath.Clean(home)
		if p == home {
			return "home directory"
		}
		sep := string(filepath.Separator)
		if strings.HasPrefix(p, home+sep) {
			return "~/" + filepath.ToSlash(strings.TrimPrefix(p, home+sep))
		}
	}
	return filepath.ToSlash(p)
}
