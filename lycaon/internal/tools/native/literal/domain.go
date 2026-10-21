package literal

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/fileoutline"
	reposurvey "github.com/lycaon/lycaon/internal/survey"
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

// runOutline outlines the pinned accepted text of one file.
func runOutline(inv *invocation) error {
	abs, ok, err := inv.domainPath(true)
	if err != nil || !ok {
		return err
	}
	source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, abs)
	if err != nil {
		if reason, ok := ordinary(err); ok {
			value, _ := inv.value("--path")
			inv.fail(1, value, reason)
			return nil
		}
		return err
	}
	if err := inv.tc.ObserveCredentialRead(inv.ctx, abs, source.Path, source.Content); err != nil {
		return fmt.Errorf("record credential file read: %w", err)
	}
	if err := inv.recordPin(source.Revision); err != nil {
		return err
	}
	outline, err := fileoutline.BuildContent(inv.ctx, source.Path, []byte(source.Content), int64(len(source.Content)))
	if err != nil {
		return err
	}
	return inv.structured(survey.OutlineResponse(source.Path, outline))
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
	resp, _, err := inv.owners.Domain.Summarize.Summarize(inv.ctx, survey.SummarizeRequest{Path: abs, Task: focus}, inv.tc)
	if err != nil {
		return err
	}
	return inv.structured(resp)
}

func runRepoMap(inv *invocation) error {
	if inv.owners.Domain.Repo == nil {
		return platformUnavailable("repo-map", "survey owner")
	}
	if value, ok := inv.value("--depth"); ok {
		return inv.usage(value, "the survey owner selects its own layout depth")
	}
	abs, ok, err := inv.domainPath(false)
	if err != nil || !ok {
		return err
	}
	resp, err := inv.owners.Domain.Repo.Survey(inv.ctx, repoMapBundle, abs, inv.tc)
	if err != nil {
		return err
	}
	return inv.structured(resp)
}
