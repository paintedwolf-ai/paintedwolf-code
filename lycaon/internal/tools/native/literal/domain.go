package literal

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/fileoutline"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// DomainOwners are the retained structured domain operations the outline,
// summarize and repo-map built-ins share. Their stdout is the explicit
// structured result, never a substitute for literal cat or grep output.
type DomainOwners struct {
	Summarize *survey.SummarizeTool
	Repo      *reposurvey.RepoTool
}

// repoMapBundle is the layout map repo-map selects from the survey catalog.
const repoMapBundle = "layout_overview"

// domainPath binds --path through the invocation cwd and returns the
// absolute address the domain owner resolves under its own authority.
func (inv *invocation) domainPath(required bool) (string, bool, error) {
	value, ok := inv.value("--path")
	if !ok {
		if required {
			return "", false, inv.usage(inv.name(), "--path PATH")
		}
		value = "."
	}
	if len(inv.operands()) > 0 {
		return "", false, inv.usage(inv.operands()[0], "no operands")
	}
	bound, err := inv.paths.Bind(inv.ctx, value, false)
	if err != nil {
		if reason, ok := ordinary(err); ok {
			inv.fail(1, value, reason)
			return "", false, nil
		}
		return "", false, err
	}
	return bound.Resolved.Abs, true, nil
}

func (inv *invocation) structured(value any) error {
	raw, err := surveyjson.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s encode: %w", inv.name(), err)
	}
	return inv.out.text(string(raw) + "\n")
}

// runOutline outlines the pinned captured text of one file.
func runOutline(inv *invocation) error {
	if _, ok, err := inv.domainPath(true); err != nil || !ok {
		return err
	}
	operand, _ := inv.value("--path")
	in, err := inv.open(operand, true)
	if err != nil || in == nil {
		return err
	}
	text, _, err := textfile.Decode(in.data, textfile.LimitsForRaw(max(int64(len(in.data)), 1)))
	if err != nil {
		inv.fail(1, operand, "not a text file")
		return nil //nolint:nilerr -- Invalid text is an ordinary status-1 utility result, not a host failure.
	}
	label := in.bound.Resolved.DisplayPath
	outline, err := fileoutline.BuildContent(inv.ctx, label, []byte(text), int64(len(text)))
	if err != nil {
		return err
	}
	return inv.structured(survey.OutlineResponse(label, outline))
}

func runSummarize(inv *invocation) error {
	if inv.owners.Domain.Summarize == nil {
		return platformUnavailable("summarize", "summarization owner")
	}
	abs, ok, err := inv.domainPath(true)
	if err != nil || !ok {
		return err
	}
	focus, _ := inv.value("--focus")
	resp, _, err := inv.owners.Domain.Summarize.Summarize(inv.ctx, survey.SummarizeRequest{Path: abs, Task: focus, Observe: inv.pinOpened}, inv.tc)
	if err != nil {
		return err
	}
	return inv.structured(resp)
}

// runRepoMap runs the layout bundle, whose find and list_dir probes read
// catalog and directory metadata only; it opens no file content to pin.
func runRepoMap(inv *invocation) error {
	var depth *int64
	if _, ok := inv.value("--depth"); ok {
		value, err := inv.integer("--depth", 0, false)
		if err != nil {
			return err
		}
		depth = &value
	}
	if inv.owners.Domain.Repo == nil {
		return platformUnavailable("repo-map", "survey owner")
	}
	abs, ok, err := inv.domainPath(false)
	if err != nil || !ok {
		return err
	}
	resp, err := inv.owners.Domain.Repo.Survey(inv.ctx, reposurvey.RepoRequest{Bundle: repoMapBundle, Path: abs, LayoutDepth: depth}, inv.tc)
	if err != nil {
		return err
	}
	return inv.structured(resp)
}
