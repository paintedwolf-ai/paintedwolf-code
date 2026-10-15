package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/diagnostics"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/hostpower"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/systemproxy"
	"github.com/lycaon/lycaon/internal/upgradefixture"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/visualscreen/ocrfixture"
)

func runDiagnostics(ctx context.Context, args []string) error {
	if len(args) == 3 && args[0] == "upgrade-fixture-verify" {
		return upgradefixture.Verify(ctx, args[1], args[2]) //nolint:gosec // G602 — guarded by len(args) == 3
	}
	if len(args) == 1 && args[0] == "schema-baseline" {
		baseline, err := db.CurrentBaseline(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(baseline)
	}
	if len(args) == 2 && args[0] == "store-baseline" {
		return writeStoreBaseline(ctx, args[1], os.Stdout)
	}
	if len(args) == 1 && args[0] == "document-core" {
		return verifyDocumentCore(ctx)
	}
	if len(args) == 2 && args[0] == "system-proxy" {
		return describeSystemProxy(args[1], os.Stdout)
	}
	if len(args) == 2 && args[0] == "power-inhibit" {
		hold, err := time.ParseDuration(args[1])
		if err != nil || hold < 0 {
			return fmt.Errorf("power-inhibit requires a hold duration such as 30s")
		}
		return holdPowerInhibitor(ctx, hostpower.New(true), hold, os.Stdout)
	}
	if len(args) == 2 && args[0] == "git-remote" {
		return probeGitRemote(ctx, args[1], os.Stdout)
	}
	if len(args) == 1 && args[0] == "ocr-engine" {
		return verifyOCREngine(ctx, os.Stderr)
	}
	if len(args) != 1 || args[0] != "startup-diagnostics" {
		return fmt.Errorf("usage: pw diagnostics startup-diagnostics | document-core | ocr-engine | system-proxy <url> | power-inhibit <duration> | git-remote <url> | schema-baseline | store-baseline <store.db> | upgrade-fixture-verify <config-dir> <manifest.json>")
	}
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("diagnostics config directory: %w", err)
	}
	return writeStartupDiagnostics(ctx, args, dir, os.Stdout, time.Now())
}

// describeSystemProxy prints the proxy host clients would use for a URL, with
// credentials removed, or the reason the configured mechanism is refused.
func describeSystemProxy(raw string, out io.Writer) error {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("system-proxy requires an absolute URL")
	}
	proxy, err := httpclient.ResolveProxy(target)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", systemproxy.Redact(proxy))
	return err
}

// holdPowerInhibitor acquires the host's sleep inhibitor as active work would,
// reports which provider holds it, and keeps it for the requested time so the
// desktop's own inhibitor listing can be checked alongside.
func holdPowerInhibitor(ctx context.Context, controller *hostpower.Controller, hold time.Duration, out io.Writer) error {
	defer func() { _ = controller.Close() }()
	controller.SetActive("diagnostics", true)
	status := controller.Snapshot()
	if _, err := fmt.Fprintf(out, "supported=%t inhibiting=%t provider=%q error=%q unavailable=%q\n", status.Supported, status.Inhibiting, status.Provider, status.LastError, status.Unavailable); err != nil {
		return err
	}
	if !status.Inhibiting {
		reason := status.Unavailable
		if reason == "" {
			reason = status.LastError
		}
		return fmt.Errorf("sleep inhibition is unavailable on this host: %s", reason)
	}
	timer := time.NewTimer(hold)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// probeGitRemote lists a remote's refs through the bundled Git exactly as a
// network operation would run it: hermetic configuration, the discovered SSH
// agent, and the host credential helper. Output is the ref count or the error.
func probeGitRemote(ctx context.Context, remote string, out io.Writer) error {
	if err := gitargv.ValidateCloneURL(remote); err != nil {
		return err
	}
	gitexec.SetHostConfig(gitexec.HostConfigResolver())
	plan, err := gitexec.NetworkProxyPlan(remote)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, describeProxyPlan(plan)); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "pw-git-remote-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	output, code, err := gitexec.Run(ctx, dir, []string{"ls-remote", "--", remote}, gitexec.Opts{Profile: gitexec.ProfileNetwork, RemoteURL: remote})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("git ls-remote exited %d: %s", code, strings.TrimSpace(string(output)))
	}
	refs := 0
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) != "" {
			refs++
		}
	}
	_, err = fmt.Fprintf(out, "refs=%d\n", refs)
	return err
}

// describeProxyPlan names how Git receives the proxy policy, the variables
// it sets, and the facts that kept it per remote. Proxy credentials never appear.
func describeProxyPlan(plan gitexec.ProxyPlan) string {
	line := "proxy_plan=" + string(plan.Mode)
	for _, variable := range plan.Environment() {
		name, value, _ := strings.Cut(variable, "=")
		if name != strings.ToLower(name) || name == "all_proxy" {
			continue
		}
		if name != "no_proxy" && value != "" {
			if proxy, err := url.Parse(value); err == nil {
				value = systemproxy.Redact(proxy)
			}
		}
		line += fmt.Sprintf(" %s=%q", name, value)
	}
	for _, reason := range plan.Reasons {
		line += fmt.Sprintf(" %s=%q", reason.Kind, reason.Detail)
	}
	return line
}

// verifyDocumentCore proves the installed document core runs from this
// executable, under the confinement and code signing editors run it with.
func verifyDocumentCore(ctx context.Context) error {
	verified, err := documentcore.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "document core verified: %s (confined=%t)\n", verified.Binary, verified.Confined)
	return nil
}

// ocrProbeLines is the text the engine self-check must read back.
var ocrProbeLines = []string{"Painted Wolf Code", "reads this image"}

// verifyOCREngine proves the installed text recognizer resolves from this
// executable and reads a rendered fixture, under the confinement images are
// screened with.
func verifyOCREngine(ctx context.Context, out io.Writer) error {
	backend, err := visualscreen.DefaultOCRBackend()
	if err != nil {
		return err
	}
	img, _, err := ocrfixture.Render(ocrfixture.Options{Lines: ocrProbeLines})
	if err != nil {
		return fmt.Errorf("render fixture: %w", err)
	}
	raw, err := ocrfixture.PNG(img)
	if err != nil {
		return fmt.Errorf("encode fixture: %w", err)
	}
	spans, err := visualscreen.NewDefaultOCREngine().RecognizeText(ctx, "image/png", raw)
	if err != nil {
		return fmt.Errorf("recognize fixture: %w", err)
	}
	var texts []string
	for _, span := range spans {
		texts = append(texts, span.Text)
	}
	read := strings.Join(texts, "\n")
	for _, word := range []string{"Painted", "Wolf", "image"} {
		if !strings.Contains(read, word) {
			return fmt.Errorf("text recognition read %q from the fixture; %q is missing", read, word)
		}
	}
	fmt.Fprintf(out, "text recognition verified: %s (%d spans)\n", backend, len(spans))
	return nil
}

// writeStartupDiagnostics writes one startup diagnostics archive.
func writeStartupDiagnostics(
	ctx context.Context,
	args []string,
	configDir string,
	out io.Writer,
	generatedAt time.Time,
) error {
	if len(args) != 1 || args[0] != "startup-diagnostics" {
		return fmt.Errorf("usage: pw diagnostics startup-diagnostics")
	}
	raw, err := diagnostics.BuildStartup(ctx, configDir, generatedAt)
	if err != nil {
		return err
	}
	if _, err := out.Write(raw); err != nil {
		return fmt.Errorf("write startup diagnostics: %w", err)
	}
	return nil
}
