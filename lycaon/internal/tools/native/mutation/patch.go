package mutation

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// patchSection is one file section bound to its targets and computed result.
type patchSection struct {
	file    patchapply.File
	display string
	target  PatchTarget
	text    patchapply.Text
	change  PatchResult
	// from is the earlier section whose result this one continues.
	from *patchSection
	// skipped holds GNU's reason the whole section was ignored.
	skipped string
	// keepReason is set when a deletion left content behind.
	keepReason string
}

// runPatch is GNU patch over the mutation owners: every section is parsed,
// captured and computed before the first file publishes, and each file then
// lands through the pinned write door at its captured revision. It never
// writes .rej or .orig files; failed hunks go to stderr in GNU's wording and
// to the receipt's evidence.
func runPatch(inv *invocation) error {
	strip, stripSet, err := inv.count("-p")
	if err != nil {
		return err
	}
	fuzz, fuzzSet, err := inv.count("-F")
	if err != nil {
		return err
	}
	if !fuzzSet {
		fuzz = 2
	}
	operands := inv.operands()
	if len(operands) > 2 {
		return inv.usage(operands[2], "at most ORIGFILE and PATCHFILE")
	}
	dir, _ := inv.value("-d")
	input, err := inv.patchInput(dir, operands)
	if err != nil {
		return err
	}
	parsed, err := patchapply.Parse(input)
	if errors.Is(err, patchapply.ErrNoPatch) {
		_, _ = fmt.Fprintln(inv.stderr, "patch: **** Only garbage was found in the patch input.")
		inv.status = 2
		return nil
	}
	if err != nil {
		_, _ = fmt.Fprintf(inv.stderr, "patch: **** malformed patch: %v\n", err)
		inv.status = 2
		return nil
	}
	sections := make([]*patchSection, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		if file.IsBinary {
			return inv.unsupportedForm("GIT binary patch", "git apply for Git binary patches")
		}
		if file.Symlink() {
			return inv.unsupportedForm("symbolic link patch", "a regular file patch")
		}
		if inv.flag("-R") {
			if file, err = patchapply.Reverse(file); err != nil {
				return err
			}
		}
		section, err := inv.bindPatchSection(file, dir, operands, strip, stripSet)
		if err != nil {
			return err
		}
		sections = append(sections, section)
	}
	if err := inv.capturePatch(sections); err != nil {
		return err
	}
	failures := []patchapply.Failure{}
	for _, section := range sections {
		inv.computePatchSection(section, patchapply.Options{Fuzz: fuzz})
		failures = append(failures, section.text.Failures(section.display)...)
	}
	if len(failures) > 0 {
		inv.evidence = toolcontract.OperationEvidence{Kind: patchapply.EvidenceKind, EvidenceRef: patchapply.EvidenceRef(failures)}
	}
	for _, section := range sections {
		if err := inv.publishPatchSection(section); err != nil {
			return err
		}
	}
	return nil
}

// capturePatch pins every section's input and destination before anything
// is computed. A later section on a file an earlier one produces continues
// from that result: it takes over the earlier capture and publication.
func (inv *invocation) capturePatch(sections []*patchSection) error {
	produced := map[string]*patchSection{}
	execution := inv.request.Invocation.Identity.ExecutionID
	for _, section := range sections {
		if earlier, ok := produced[section.target.OldAbs]; ok && section.target.OldAbs != "" {
			section.from = earlier
			section.target = PatchTarget{OldAbs: earlier.target.OldAbs, NewAbs: section.target.NewAbs, Exists: earlier.target.Exists, source: earlier.target.source, destination: earlier.target.destination}
		} else {
			target, err := inv.owners.CapturePatchTarget(inv.ctx, inv.tc, execution, section.target.OldAbs, section.target.NewAbs)
			if err != nil && !errors.Is(err, ErrPatchTargetMissing) {
				return err
			}
			section.target = target
		}
		if section.target.NewAbs != "" {
			produced[section.target.NewAbs] = section
		}
	}
	return nil
}

// computePatchSection applies one section to its captured bytes and decides
// GNU's outcome: applied, partly failed, skipped as already applied, or
// skipped for a missing or already existing file.
func (inv *invocation) computePatchSection(section *patchSection, opts patchapply.Options) {
	file, target := section.file, section.target
	hunks := len(file.TextFragments)
	if from := section.from; from != nil {
		// The earlier result is now this section's input; only this one lands.
		if from.change.Write {
			target.Before, target.Exists = from.change.After, true
		} else {
			target.Before, target.Exists = from.target.Before, from.target.Exists
		}
		from.change = PatchResult{}
	}
	switch {
	case section.from != nil:
	case file.IsNew && target.NewExists && !inv.emptyFile(target.NewAbs):
		section.skipped = fmt.Sprintf("The next patch would create the file %s,\nwhich already exists!  Skipping patch.", section.display)
		section.text = ignoredHunks(file)
		return
	case !file.IsNew && !target.Exists:
		section.skipped = fmt.Sprintf("can't find file to patch: %s\nPerhaps you used the wrong -p or --strip option?\nNo file to patch.  Skipping patch.", section.display)
		section.text = ignoredHunks(file)
		return
	}
	text, _ := file.Apply(target.Before, opts)
	if hunks > 0 && !text.Hunks[0].Applied && !inv.flag("-f") {
		if reversed, err := patchapply.Reverse(file); err == nil && patchapply.Applies(target.Before, reversed.TextFragments[:1], opts) {
			prefix := "Reversed"
			if inv.flag("-R") {
				prefix = "Unreversed"
			}
			if !inv.flag("-t") || inv.flag("-N") {
				section.skipped = prefix + " (or previously applied) patch detected!  Skipping patch."
				section.text = ignoredHunks(file)
				return
			}
			_, _ = fmt.Fprintf(inv.stderr, "%s (or previously applied) patch detected!  Assuming -R.\n", prefix)
			section.file = reversed
			file = reversed
			text, _ = file.Apply(target.Before, opts)
		}
	}
	section.text = text
	if text.Failed == hunks && hunks > 0 {
		return
	}
	remove := file.IsDelete || (inv.flag("-E") && len(text.Content) == 0)
	if file.IsDelete && len(text.Content) > 0 {
		section.keepReason = fmt.Sprintf("Not deleting file %s as content differs from patch", section.display)
		remove = false
	}
	renamed := target.OldAbs != "" && target.NewAbs != target.OldAbs && !file.IsCopy
	section.change = PatchResult{After: text.Content, Write: !remove, RemoveOld: remove || renamed}
	if section.change.Write {
		section.change.Mode = file.ModeChange()
	}
}

// emptyFile reports an existing empty file, which a creation may fill.
func (inv *invocation) emptyFile(abs string) bool {
	info, err := os.Stat(abs)
	return err == nil && info.Mode().IsRegular() && info.Size() == 0
}

func ignoredHunks(file patchapply.File) patchapply.Text {
	text := patchapply.Text{Failed: len(file.TextFragments)}
	for i, frag := range file.TextFragments {
		text.Hunks = append(text.Hunks, patchapply.Hunk{Number: i + 1, Header: frag.Header(), Line: int(frag.OldPosition), Reason: "ignored"})
	}
	return text
}

// publishPatchSection reports one section in GNU's wording and, outside
// --dry-run, lands its result.
func (inv *invocation) publishPatchSection(section *patchSection) error {
	quiet := inv.flag("-s")
	verb := "patching"
	if inv.flag("--dry-run") {
		verb = "checking"
	}
	if !quiet {
		_, _ = fmt.Fprintf(inv.stdout, "%s file %s\n", verb, section.display)
	}
	text := section.text
	hunks := len(text.Hunks)
	if section.skipped != "" {
		_, _ = fmt.Fprintf(inv.stderr, "%s\n%d out of %d %s ignored\n", section.skipped, hunks, hunks, plural(hunks, "hunk"))
		inv.status = max(inv.status, 1)
		return nil
	}
	for _, hunk := range text.Hunks {
		switch {
		case !hunk.Applied:
			_, _ = fmt.Fprintf(inv.stderr, "Hunk #%d FAILED at %d.\n", hunk.Number, hunk.Line)
		case (hunk.Fuzz > 0 || hunk.Offset != 0) && !quiet:
			var msg bytes.Buffer
			fmt.Fprintf(&msg, "Hunk #%d succeeded at %d", hunk.Number, hunk.Line)
			if hunk.Fuzz > 0 {
				fmt.Fprintf(&msg, " with fuzz %d", hunk.Fuzz)
			}
			if hunk.Offset != 0 {
				fmt.Fprintf(&msg, " (offset %d %s)", hunk.Offset, plural(abs(hunk.Offset), "line"))
			}
			_, _ = fmt.Fprintf(inv.stdout, "%s.\n", msg.String())
		}
	}
	if text.Failed > 0 {
		_, _ = fmt.Fprintf(inv.stderr, "%d out of %d %s FAILED\n", text.Failed, hunks, plural(hunks, "hunk"))
		inv.status = max(inv.status, 1)
	}
	if section.keepReason != "" {
		_, _ = fmt.Fprintln(inv.stderr, section.keepReason)
		inv.status = max(inv.status, 1)
	}
	if inv.flag("--dry-run") || (!section.change.Write && !section.change.RemoveOld) {
		return nil
	}
	effect, err := inv.owners.PublishPatchTarget(inv.ctx, inv.tc, section.target, section.change)
	if effect.Wrote || effect.Removed {
		inv.effects++
	}
	return err
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
