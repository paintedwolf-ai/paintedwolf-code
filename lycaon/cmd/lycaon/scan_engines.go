package main

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

func runScanEngines(args []string) error {
	if len(args) > 0 && args[0] == "verify-bundled" {
		return runVerifyBundledEngine(args[1:])
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: pw scan engines {list|check|validate|verify-bundled} [options]")
	}
	moduleRoot := configlayout.FindModuleRoot()
	projectDir, rest, err := parseProjectFlag(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: pw scan engines {list|check|validate|verify-bundled} [options]")
	}
	switch rest[0] {
	case "list":
		return runScanEnginesList(moduleRoot, projectDir)
	case "check":
		return runScanEnginesCheck(moduleRoot, projectDir, rest[1:])
	case "validate":
		return runScanEnginesValidate(moduleRoot, projectDir, rest[1:])
	default:
		return fmt.Errorf("unknown scan engines command %q", rest[0])
	}
}

func parseProjectFlag(args []string) (projectDir string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--project" {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--project requires a path")
			}
			projectDir = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return projectDir, rest, nil
}

func runScanEnginesList(moduleRoot, projectDir string) error {
	cfg, err := scancatalog.LoadMergedScannerConfig(moduleRoot, projectDir, "")
	if err != nil {
		return err
	}
	for _, entry := range scancatalog.ListCatalogEntries(cfg, moduleRoot, projectDir) {
		binaryFound := "n/a"
		if entry.Driver == scancatalog.DriverExternal {
			if entry.BinaryFound {
				binaryFound = "yes"
			} else {
				binaryFound = "no"
			}
		}
		fmt.Printf("%s\tdriver=%s\tenabled=%t\tbinary=%s\tcategories=%s\n",
			entry.ID, entry.Driver, entry.Enabled, binaryFound, strings.Join(entry.Categories, ","))
	}
	return nil
}

func runScanEnginesCheck(moduleRoot, projectDir string, args []string) error {
	dryRun := false
	for _, arg := range args {
		if arg == "--dry-run" {
			dryRun = true
		}
	}
	cfg, err := scancatalog.LoadMergedScannerConfig(moduleRoot, projectDir, "")
	if err != nil {
		return err
	}
	ok := true
	for _, report := range scancatalog.CheckCatalog(cfg, moduleRoot, projectDir) {
		status := "ok"
		if !report.CheckOK {
			status = "fail"
			ok = false
		}
		fmt.Printf("%s: %s\n", report.ID, status)
		if len(report.Issues) > 0 {
			for _, issue := range report.Issues {
				fmt.Printf("  - %s: %s\n", issue.Code, issue.Detail)
			}
		}
		if dryRun && len(report.ExpandedCommand) > 0 {
			fmt.Printf("  argv: %q\n", report.ExpandedCommand)
		}
	}
	if !ok {
		return fmt.Errorf("one or more scanners failed install checks")
	}
	return nil
}

func runScanEnginesValidate(moduleRoot, projectDir string, args []string) error {
	_ = args
	_, err := scancatalog.LoadMergedScannerConfig(moduleRoot, projectDir, "")
	return err
}
