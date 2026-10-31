package query

import (
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	"strings"
)

// runJqEdit runs the structured jq_edit's format-preserving edit over the
// pinned accepted text and publishes the result at the captured destination
// revision. A lossy rewrite refuses (JQ_EDIT_LOSSY); a moved source or
// destination conflicts and nothing is refreshed or rerun. Unlike a plain
// jq redirect, unchanged literals, key order and comments survive.
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
	edited, err := nativejq.Edit(inv.ctx, nativejq.EditRequest{Path: bound.Resolved.DisplayPath, Format: format, Query: query, Text: source.text})
	if err != nil {
		return err
	}
	if edited.Text == source.text {
		return nil
	}
	return inv.publishText(source, destination, edited.Text, work)
}
