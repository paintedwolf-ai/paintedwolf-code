package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/theme"
)

func runExtensions(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw extensions {install|install-meta|lock|remove|remove-meta|validate|inspect-command|new-theme|apply-profile|disable|enable|enable-suite|disable-suite|own|update|reload} …")
	}
	switch args[0] {
	case "install":
		return runExtInstall(ctx, args[1:])
	case "install-meta":
		return runExtInstallMeta(ctx, args[1:])
	case "lock":
		return runExtLock(ctx, args[1:])
	case "remove":
		return runExtRemove(ctx, args[1:])
	case "remove-meta":
		return runExtRemoveMeta(ctx, args[1:])
	case "validate":
		return runExtValidate(ctx, args[1:])
	case "inspect-command":
		return runExtInspectCommand(ctx, args[1:])
	case "apply-profile":
		return runExtApplyProfile(ctx, args[1:])
	case "disable":
		return runExtDisable(ctx, args[1:])
	case "enable":
		return runExtEnable(ctx, args[1:])
	case "enable-suite":
		return runExtEnableSuite(ctx, args[1:])
	case "disable-suite":
		return runExtDisableSuite(ctx, args[1:])
	case "own":
		return runExtOwn(ctx, args[1:])
	case "update":
		return runExtUpdate(ctx, args[1:])
	case "reload":
		return runExtReload(ctx, args[1:])
	case "new-theme":
		return runExtNewTheme(args[1:])
	default:
		return fmt.Errorf("unknown extensions command %q", args[0])
	}
}

type commandInspection struct {
	ID                 string                       `json:"id"`
	Provider           string                       `json:"provider"`
	Palette            bool                         `json:"palette"`
	When               *contribution.Condition      `json:"when,omitempty"`
	Enablement         *contribution.Condition      `json:"enablement,omitempty"`
	Input              []contribution.InputField    `json:"input"`
	Output             []contribution.OutputField   `json:"output,omitempty"`
	Interaction        *contribution.Interaction    `json:"interaction,omitempty"`
	Executor           contribution.Executor        `json:"executor"`
	Invocation         contribution.InvocationScope `json:"invocation"`
	Action             contribution.Action          `json:"action"`
	Result             contribution.ResultTreatment `json:"result"`
	Icon               string                       `json:"icon"`
	Menus              []*contribution.Menu         `json:"menus"`
	Keybindings        []*contribution.Keybinding   `json:"keybindings"`
	OperationChain     []string                     `json:"operation_chain,omitempty"`
	OperationConsumers map[string][]string          `json:"operation_consumers,omitempty"`
	CatalogRevision    string                       `json:"catalog_revision"`
}

func runExtInspectCommand(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagContext|extFlagJSON); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: pw extensions inspect-command <command-id> [--project DIR|--device] [--json]")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions inspect-command <command-id> [--project DIR|--device] [--json]"); err != nil {
		return err
	}
	id, err := contribution.ParseID(pos[0])
	if err != nil {
		return err
	}
	moduleRoot := configlayout.FindModuleRoot()
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return err
	}
	resolvedCatalog, err := extpacks.ResolveCatalog(ctx, []string{flags.projectDir}, scan.RequirementChecker{ModuleRoot: moduleRoot, HomeDir: homeDir})
	if err != nil {
		return err
	}
	eff := catalogview.NewCache(moduleRoot, nil).InForce(ctx, resolvedCatalog)
	view, err := catalogview.Build(ctx, moduleRoot, eff)
	if err != nil {
		return err
	}
	command, ok := view.Contributions.Command(id)
	if !ok {
		return fmt.Errorf("command %s is not in the compiled catalog", id)
	}
	resolved, ok := view.Contributions.ResolveCommand(command)
	if !ok {
		return fmt.Errorf("command %s has no resolved operation contract", id)
	}
	chain := make([]string, 0, len(resolved.Chain))
	consumers := map[string][]string{}
	for _, operationID := range resolved.Chain {
		chain = append(chain, operationID.String())
		consumers[operationID.String()] = operationConsumers(view.Contributions, operationID)
	}
	inspection := commandInspection{
		ID: command.ID, Provider: id.Provider, Palette: command.InPalette(), When: command.When,
		Enablement: command.Enablement, Input: resolved.Input, Output: resolved.Output, Interaction: command.Interaction,
		Executor: contribution.ExecutorFor(resolved.Action.Kind), Invocation: contribution.InvocationScopeFor(resolved.Action.Kind),
		Action: resolved.Action, Result: resolved.Result, Icon: resolved.Icon, OperationChain: chain,
		OperationConsumers: consumers, CatalogRevision: eff.Revision,
		Menus: []*contribution.Menu{}, Keybindings: []*contribution.Keybinding{},
	}
	for _, menu := range view.Contributions.Menus() {
		if menu.Command == command.ID {
			inspection.Menus = append(inspection.Menus, menu)
		}
	}
	for _, binding := range view.Contributions.Keybindings() {
		if binding.Command == command.ID {
			inspection.Keybindings = append(inspection.Keybindings, binding)
		}
	}
	if flags.jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(inspection)
	}
	encoded, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func operationConsumers(set *contribution.Set, operationID contribution.ID) []string {
	var consumers []string
	for _, command := range set.Commands() {
		resolved, ok := set.ResolveCommand(command)
		if !ok {
			continue
		}
		for _, candidate := range resolved.Chain {
			if candidate == operationID {
				consumers = append(consumers, command.ID)
				break
			}
		}
	}
	sort.Strings(consumers)
	return consumers
}

type extCommonFlags struct {
	projectDir string
	jsonOut    bool
	version    string
	ref        string
	explicit   extFlagSet
}

type extFlagSet uint8

const (
	extFlagContext extFlagSet = 1 << iota
	extFlagJSON
	extFlagVersion
	extFlagRef
)

func parseExtFlags(args []string) (extCommonFlags, []string, error) {
	var f extCommonFlags
	var positional []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		switch arg {
		case "--project":
			if f.explicit&extFlagContext != 0 {
				return f, nil, fmt.Errorf("extension context may be set once")
			}
			if len(args) == 0 {
				return f, nil, fmt.Errorf("--project requires a directory")
			}
			f.projectDir = strings.TrimSpace(args[0])
			args = args[1:]
			if f.projectDir == "" {
				return f, nil, fmt.Errorf("--project requires a directory")
			}
			f.explicit |= extFlagContext
		case "--device":
			if f.explicit&extFlagContext != 0 {
				return f, nil, fmt.Errorf("extension context may be set once")
			}
			f.explicit |= extFlagContext
		case "--json":
			f.jsonOut = true
			f.explicit |= extFlagJSON
		case "--ref":
			if len(args) == 0 {
				return f, nil, fmt.Errorf("--ref requires a value")
			}
			f.ref = strings.TrimSpace(args[0])
			args = args[1:]
			if f.ref == "" {
				return f, nil, fmt.Errorf("--ref requires a value")
			}
			f.explicit |= extFlagRef
		case "--version":
			if len(args) == 0 {
				return f, nil, fmt.Errorf("--version requires a SemVer constraint")
			}
			f.version = strings.TrimSpace(args[0])
			args = args[1:]
			if f.version == "" {
				return f, nil, fmt.Errorf("--version requires a SemVer constraint")
			}
			f.explicit |= extFlagVersion
		default:
			if strings.HasPrefix(arg, "-") {
				return f, nil, fmt.Errorf("unknown flag %q", arg)
			}
			positional = append(positional, arg)
		}
	}
	return f, positional, nil
}

func rejectUnsupportedExtFlags(flags extCommonFlags, allowed extFlagSet) error {
	unsupported := flags.explicit &^ allowed
	for _, candidate := range []struct {
		flag  extFlagSet
		label string
	}{
		{extFlagContext, "--project/--device"},
		{extFlagJSON, "--json"},
		{extFlagVersion, "--version"},
		{extFlagRef, "--ref"},
	} {
		if unsupported&candidate.flag != 0 {
			return fmt.Errorf("%s is not supported by this command", candidate.label)
		}
	}
	return nil
}

func rejectExtraArgs(pos []string, want int, usage string) error {
	if len(pos) > want {
		return fmt.Errorf("unexpected argument %q; usage: %s", pos[want], usage)
	}
	return nil
}

func runExtInstall(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagJSON|extFlagVersion|extFlagRef); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions install <url|file://|path:…> [--version RANGE|--ref REF]")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions install <url|file://|path:…> [--version RANGE|--ref REF]"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.InstallOp{Source: pos[0], Version: flags.version, Ref: flags.ref})
	if err != nil {
		return err
	}
	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if res.Install != nil {
		fmt.Printf("installed %s → %s\n", res.Install.PackID, res.PackageRoot)
		printPackageChanges(res.PackageChanges)
	}
	fmt.Printf("desired: %s\n", res.DesiredPath)
	reportWarnings(res)
	return nil
}

func runExtInstallMeta(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagJSON|extFlagVersion|extFlagRef); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions install-meta <url|file://|path:…> [--version RANGE|--ref REF]")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions install-meta <url|file://|path:…> [--version RANGE|--ref REF]"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.InstallMetaOp{Source: pos[0], Version: flags.version, Ref: flags.ref})
	if err != nil {
		return err
	}
	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if res.Meta != nil {
		fmt.Printf("installed meta-pack %s → %s\n", res.Meta.MetaPackID, res.Meta.MetaRoot)
		fmt.Printf("members: %s\n", strings.Join(res.Meta.MemberPackIDs, ", "))
	}
	fmt.Printf("desired: %s\n", res.DesiredPath)
	reportWarnings(res)
	return nil
}

func runExtLock(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagJSON); err != nil {
		return err
	}
	if err := rejectExtraArgs(pos, 0, "pw extensions lock"); err != nil {
		return err
	}
	result, err := applyDeviceExtensionIntent(ctx, extensionstate.LockOp{})
	if err != nil {
		return err
	}
	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Printf("locked desired roots\ndesired: %s\n", result.DesiredPath)
	reportWarnings(result)
	return nil
}

func runExtRemove(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions remove <pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions remove <pack-id>"); err != nil {
		return err
	}
	if _, err := applyDeviceExtensionIntent(ctx,
		extensionstate.RemoveOp{PackID: pos[0]}); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", pos[0])
	return nil
}

func runExtRemoveMeta(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions remove-meta <meta-pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions remove-meta <meta-pack-id>"); err != nil {
		return err
	}
	if _, err := applyDeviceExtensionIntent(ctx,
		extensionstate.RemoveMetaOp{MetaPackID: pos[0]}); err != nil {
		return err
	}
	fmt.Printf("removed meta-pack %s\n", pos[0])
	return nil
}

func runExtEnableSuite(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions enable-suite <meta-pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions enable-suite <meta-pack-id>"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.ApplyMetaOp{MetaPackID: pos[0], Enable: true})
	if err != nil {
		return err
	}
	fmt.Printf("enabled suite %s → %s\n", pos[0], res.DesiredPath)
	reportWarnings(res)
	return nil
}

func runExtDisableSuite(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions disable-suite <meta-pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions disable-suite <meta-pack-id>"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.ApplyMetaOp{MetaPackID: pos[0], Enable: false})
	if err != nil {
		return err
	}
	fmt.Printf("disabled suite %s → %s\n", pos[0], res.DesiredPath)
	reportWarnings(res)
	return nil
}

func runExtValidate(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagContext|extFlagJSON); err != nil {
		return err
	}
	if err := rejectExtraArgs(pos, 0, "pw extensions validate [--project DIR|--device] [--json]"); err != nil {
		return err
	}
	moduleRoot := configlayout.FindModuleRoot()
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return err
	}
	resolved, err := extpacks.ResolveCatalog(ctx, []string{flags.projectDir},
		scan.RequirementChecker{ModuleRoot: moduleRoot, HomeDir: homeDir})
	if err != nil {
		return err
	}
	// Validate the catalog that runtime would apply.
	views := catalogview.NewCache(moduleRoot, nil)
	eff := views.InForce(ctx, resolved)
	rep, err := extpacks.Validate(ctx, flags.projectDir, eff)
	if err != nil {
		return err
	}
	graph := validateContributionGraph(ctx, moduleRoot, flags.projectDir, eff)
	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			*extpacks.ValidateReport
			ContributionFaults []contribution.Fault `json:"contribution_faults,omitempty"`
			ContributionNotes  []contribution.Note  `json:"contribution_notes,omitempty"`
			PersonaViolations  []string             `json:"persona_violations,omitempty"`
			VocabularyNotes    []string             `json:"vocabulary_notes,omitempty"`
			ThemeContrast      []themeContrastRow   `json:"theme_contrast,omitempty"`
		}{rep, graph.faults, graph.notes, graph.personas, graph.vocabulary, graph.contrast}); err != nil {
			return err
		}
	} else {
		fmt.Print(extpacks.FormatValidateText(rep))
		for _, v := range graph.personas {
			fmt.Printf("persona %s\n", v)
		}
		for _, note := range graph.vocabulary {
			fmt.Printf("tool vocabulary: %s\n", note)
		}
		for _, fault := range graph.faults {
			fmt.Printf("contribution %s: %s: %s (%s)\n", fault.Code, fault.UnitID, fault.Message, fault.PackID)
		}
		for _, note := range graph.notes {
			fmt.Printf("note %s: %s\n", note.Code, note.Message)
		}
		printThemeContrast(graph.contrast)
	}
	// Persona violations fail validation.
	if !rep.OK || len(graph.faults) > 0 || len(graph.personas) > 0 {
		os.Exit(1)
	}
	return nil
}

type contributionGraphReport struct {
	faults []contribution.Fault
	// personas contains rendered prompt violations.
	personas []string
	// vocabulary contains unreachable rule copy.
	vocabulary []string
	notes      []contribution.Note
	contrast   []themeContrastRow
}

// validatePackPersonas checks rendered prompts from installed packs.
func validatePackPersonas(
	ctx context.Context,
	moduleRoot, projectDir string,
	eff *extpacks.EffectiveCatalog,
) []string {
	var out []string
	for _, id := range packAgentIDs(eff) {
		res, err := prompts.AuthorRender(ctx, prompts.AuthorRenderRequest{
			ModuleRoot: moduleRoot,
			ProjectDir: projectDir,
			AgentID:    id,
			Check:      true,
		})
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		for _, v := range res.Violations {
			out = append(out, fmt.Sprintf("%s: %s", id, v))
		}
	}
	sort.Strings(out)
	return out
}

// packAgentIDs lists the agents non-stock packs contribute, in catalog order.
func packAgentIDs(eff *extpacks.EffectiveCatalog) []string {
	var ids []string
	for _, unitID := range eff.LoadedUnitIDs() {
		id, ok := strings.CutPrefix(unitID, "agents/")
		if !ok || strings.Contains(id, "/") {
			continue
		}
		if eff.StockAuthority(eff.Loaded[unitID].WinnerPackID) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// themeContrastRow is one measured pair for the legibility report.
type themeContrastRow struct {
	Theme      string  `json:"theme"`
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Ratio      float64 `json:"ratio"`
	Minimum    float64 `json:"minimum"`
	Passes     bool    `json:"passes"`
}

// validateContributionGraph uses the runtime catalog compiler.
func validateContributionGraph(
	ctx context.Context,
	moduleRoot, projectDir string,
	eff *extpacks.EffectiveCatalog,
) contributionGraphReport {
	view, err := catalogview.Build(ctx, moduleRoot, eff)
	if err != nil {
		var compileErr *contribution.CompileError
		if errors.As(err, &compileErr) {
			return contributionGraphReport{faults: compileErr.Faults}
		}
		return contributionGraphReport{faults: []contribution.Fault{{
			Code: "view_build_failed", Message: err.Error(),
		}}}
	}
	return contributionGraphReport{
		notes:      view.Contributions.Notes(),
		vocabulary: view.VocabularyNotes,
		personas:   validatePackPersonas(ctx, moduleRoot, projectDir, eff),
		contrast:   measureThemeContrast(view.Contributions.Themes()),
	}
}

func runExtApplyProfile(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: pw extensions apply-profile <pack-id> <profile-name>")
	}
	if err := rejectExtraArgs(pos, 2, "pw extensions apply-profile <pack-id> <profile-name>"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.ApplyProfileOp{PackID: pos[0], Profile: pos[1]})
	if err != nil {
		return err
	}
	fmt.Printf("applied profile %s/%s → %s\n", pos[0], pos[1], res.DesiredPath)
	reportWarnings(res)
	return nil
}

func runExtDisable(ctx context.Context, args []string) error {
	return runExtSetEnabled(ctx, args, false)
}

func runExtEnable(ctx context.Context, args []string) error {
	return runExtSetEnabled(ctx, args, true)
}

// Unit ids contain an inventory kind root; pack ids do not.
func runExtSetEnabled(ctx context.Context, args []string, enabled bool) error {
	verb := "enabled"
	if !enabled {
		verb = "disabled"
	}
	usage := fmt.Sprintf("pw extensions %s <pack-id|unit-id> [--project DIR|--device]",
		strings.TrimSuffix(verb, "d"))
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, extFlagContext); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if err := rejectExtraArgs(pos, 1, usage); err != nil {
		return err
	}
	id := strings.TrimSpace(pos[0])
	unitKind := extpacks.KindRootForUnitID(id)
	if flags.projectDir != "" && unitKind == "" {
		return fmt.Errorf("project Trust can disable extension units, not packs")
	}
	var res extensionstate.Result
	if flags.projectDir != "" {
		res, err = applyProjectUnitIntent(ctx, flags.projectDir,
			extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: !enabled})
	} else if unitKind != "" {
		res, err = applyDeviceExtensionIntent(ctx,
			extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: !enabled})
	} else {
		res, err = applyDeviceExtensionIntent(ctx,
			extensionstate.SetPackEnabledOp{PackID: id, Enabled: enabled})
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s %s in %s\n", verb, id, res.DesiredPath)
	reportWarnings(res)
	return nil
}

// reportWarnings prints packs isolated by a mutation.
func reportWarnings(res extensionstate.Result) {
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}

func runExtOwn(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: pw extensions own <unit-id> <pack-id>")
	}
	if err := rejectExtraArgs(pos, 2, "pw extensions own <unit-id> <pack-id>"); err != nil {
		return err
	}
	owner := pos[1]
	res, err := applyDeviceExtensionIntent(ctx,
		extensionstate.SetUnitOwnOp{UnitID: pos[0], PackID: &owner})
	if err != nil {
		return err
	}
	fmt.Printf("own %s → %s in %s\n", pos[0], pos[1], filepath.Clean(res.DesiredPath))
	reportWarnings(res)
	return nil
}

func runExtUpdate(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions update <pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions update <pack-id>"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx, extensionstate.UpdateOp{PackID: pos[0]})
	if err != nil {
		return err
	}
	if res.Install != nil {
		fmt.Printf("updated %s revision=%s\n", pos[0], res.Install.ResolvedRevision)
	}
	printPackageChanges(res.PackageChanges)
	return nil
}

// printPackageChanges includes transitive package changes.
func printPackageChanges(changes []extpacks.PackageChange) {
	if len(changes) == 0 {
		fmt.Println("no package changes")
		return
	}
	for _, c := range changes {
		switch c.Kind {
		case "added":
			fmt.Printf("  + %s %s\n", c.PackID, c.CandidateVersion)
		case "removed":
			fmt.Printf("  - %s %s\n", c.PackID, c.CurrentVersion)
		default:
			fmt.Printf("  ~ %s %s → %s\n", c.PackID, c.CurrentVersion, c.CandidateVersion)
		}
	}
}

func runExtReload(ctx context.Context, args []string) error {
	flags, pos, err := parseExtFlags(args)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedExtFlags(flags, 0); err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: pw extensions reload <pack-id>")
	}
	if err := rejectExtraArgs(pos, 1, "pw extensions reload <pack-id>"); err != nil {
		return err
	}
	res, err := applyDeviceExtensionIntent(ctx, extensionstate.ReloadOp{PackID: pos[0]})
	if err != nil {
		return err
	}
	fmt.Printf("reloaded %s\n", pos[0])
	printPackageChanges(res.PackageChanges)
	for _, w := range res.Warnings {
		fmt.Printf("warning: %s\n", w)
	}
	return nil
}

// measureThemeContrast reports effective theme contrast margins.
func measureThemeContrast(themes []*contribution.Theme) []themeContrastRow {
	var out []themeContrastRow
	for _, declared := range themes {
		compiled, err := declared.Compiled()
		if err != nil {
			continue
		}
		for _, m := range theme.MeasureLegibility(compiled) {
			out = append(out, themeContrastRow{
				Theme:      compiled.Name,
				Foreground: m.Pair.Foreground,
				Background: m.Pair.Background,
				Ratio:      math.Round(m.Ratio*100) / 100,
				Minimum:    m.Pair.Min,
				Passes:     m.Passes,
			})
		}
	}
	return out
}

func printThemeContrast(rows []themeContrastRow) {
	if len(rows) == 0 {
		return
	}
	current := ""
	for _, row := range rows {
		if row.Theme != current {
			current = row.Theme
			fmt.Printf("theme %s — legibility floor:\n", current)
		}
		mark := "ok  "
		if !row.Passes {
			mark = "FAIL"
		}
		fmt.Printf("  %s %-16s on %-16s %6.2f:1 (min %.1f:1)\n",
			mark, row.Foreground, row.Background, row.Ratio, row.Minimum)
	}
}
