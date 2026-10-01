package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/bundleverify"
)

const bundleVerifyUsage = `usage: lycaon-debug bundle-verify --app <path.app> [--dmg <path.dmg>]
                                  [--require-signed] [--credential-probe-advisory] [--json]

Structural + signing audit of a built macOS bundle. Expected architecture
slices and the macOS floor come from lycaon/internal/platformfloor — they are
not flags, because a flag would just be a way to disagree with the SSOT.

Exit: 0 clean, 1 findings, 2 usage or IO error.`

// exitCodeError carries the process exit status a command wants. main honors it
// so bundle-verify can distinguish "findings" (1) from "bad invocation" (2).
type exitCodeError struct {
	code int
	err  error
}

func (e exitCodeError) Error() string { return e.err.Error() }
func (e exitCodeError) Unwrap() error { return e.err }

func runBundleVerify(args []string) error {
	fs := flag.NewFlagSet("bundle-verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, bundleVerifyUsage) }

	appPath := fs.String("app", "", "path to the built .app bundle (required)")
	dmgPath := fs.String("dmg", "", "path to the notarized .dmg (optional; the staple lives here)")
	requireSigned := fs.Bool("require-signed", false, "promote signature findings from warn to error")
	credentialAdvisory := fs.Bool("credential-probe-advisory", false, "report a failed Keychain protection probe as a warning")
	asJSON := fs.Bool("json", false, "emit the report as JSON")

	if err := parseStrictFlags(fs, args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if *appPath == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--app is required\n%s", bundleVerifyUsage)}
	}
	if _, err := os.Stat(*appPath); err != nil {
		return exitCodeError{code: 2, err: fmt.Errorf("--app %s: %w", *appPath, err)}
	}

	report, err := bundleverify.Verify(context.Background(), bundleverify.Options{
		AppPath:                 *appPath,
		DMGPath:                 *dmgPath,
		RequireSigned:           *requireSigned,
		CredentialProbeAdvisory: *credentialAdvisory,
	})
	if err != nil {
		return exitCodeError{code: 2, err: err}
	}

	if err := printBundleReport(report, *asJSON); err != nil {
		return exitCodeError{code: 2, err: err}
	}

	if report.Failed() {
		return exitCodeError{code: 1, err: fmt.Errorf("bundle verification failed")}
	}
	return nil
}

func printBundleReport(report bundleverify.Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	for _, f := range report.Findings {
		var detail []string
		for k, v := range f.Detail {
			detail = append(detail, k+"="+v)
		}
		sort.Strings(detail)

		line := fmt.Sprintf("%s %s %s", f.Severity, f.Code, f.Path)
		if len(detail) > 0 {
			line += " " + strings.Join(detail, " ")
		}
		fmt.Fprintln(os.Stdout, line)
	}

	errs, warns := report.Counts()
	fmt.Fprintf(os.Stdout, "%d Mach-O files, %d errors, %d warnings\n", report.MachOCount, errs, warns)
	return nil
}
