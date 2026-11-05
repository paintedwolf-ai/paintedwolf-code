package query

import (
	"strings"

	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

// runJqEdit preserves document formatting and publishes at the captured revision.
// editVarsSlot is the contract's secret_reference_args entry for --arg values.
const editVarsSlot = "vars"

// editVars are the admitted --arg values: the host's resolved copy when it ran,
// otherwise the bound words. The first definition of a name wins, as in jq.
func (inv *invocation) editVars() map[string]any {
	if resolution := inv.tc.Effects.Secrets; resolution != nil {
		if resolved, ok := resolution.Arguments[editVarsSlot].(map[string]any); ok {
			return resolved
		}
	}
	vars := map[string]any{}
	for _, item := range inv.argv.Items {
		if option := item.Option; option != nil && option.Key == "--arg" && len(option.Values) == 2 {
			if _, taken := vars[option.Values[0].Value]; !taken {
				vars[option.Values[0].Value] = option.Values[1].Value
			}
		}
	}
	return vars
}

func runJqEdit(inv *invocation) error {
	if operands := inv.argv.Operands().Values(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	pathField, ok := inv.valueField("--path")
	if !ok {
		return inv.usage(inv.name(), "--path PATH")
	}
	path := pathField.Value
	query, ok := inv.value("--query")
	if !ok {
		return inv.usage(inv.name(), "--query QUERY")
	}
	format, _ := inv.value("--format")
	bound, err := inv.paths.Bind(inv.ctx, path, true)
	if err != nil {
		return err
	}
	if bound.Null {
		return inv.usage(path, "a document PATH")
	}
	source, err := inv.capture(pathField)
	if err != nil || source == nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	work := &queryWorking{inv: inv}
	defer work.Close()
	detectedFormat, err := nativejq.DetectFormat(bound.Resolved.DisplayPath, format)
	if err != nil {
		return err
	}
	parseBytes := int64(len(source.text))
	if detectedFormat == "json" {
		parseBytes = int64(len(strings.TrimSpace(source.text)))
	}
	if err := work.resize(2*parseBytes + (64 << 10)); err != nil {
		return err
	}
	edited, err := nativejq.Edit(inv.ctx, nativejq.EditRequest{Path: bound.Resolved.DisplayPath, Format: format, Query: query, Text: source.text, Vars: inv.editVars()})
	if err != nil {
		return err
	}
	if edited.Text == source.text {
		return nil
	}
	return inv.publishText(source, destination, edited.Text, work)
}
