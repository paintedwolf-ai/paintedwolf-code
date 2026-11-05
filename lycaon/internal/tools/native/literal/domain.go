package literal

import (
	"fmt"
	"slices"

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

// repoMapBundle is the catalog map repo-map runs when --bundle is absent.
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
	return inv.bindDomainPath(value)
}

func (inv *invocation) bindDomainPath(value string) (string, bool, error) {
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
	if err == nil {
		label := in.bound.Resolved.DisplayPath
		outline, buildErr := fileoutline.BuildContent(inv.ctx, label, []byte(text), int64(len(text)))
		if buildErr != nil {
			return buildErr
		}
		return inv.structured(survey.OutlineResponse(label, outline))
	}
	inv.fail(1, operand, "not a text file")
	return nil
}

func runSummarize(inv *invocation) error {
	if inv.owners.Domain.Summarize == nil {
		return platformUnavailable("summarize", "summarization owner")
	}
	if len(inv.operands()) > 0 {
		return inv.usage(inv.operands()[0], "no operands")
	}
	var paths []string
	for _, option := range inv.argv.Options("--path") {
		for _, value := range option.Values {
			abs, ok, err := inv.bindDomainPath(value.Value)
			if err != nil || !ok {
				return err
			}
			paths = append(paths, abs)
		}
	}
	content, hasContent := inv.value("--content")
	if len(paths) == 0 && !hasContent {
		return inv.usage(inv.name(), "--path PATH or --content TEXT")
	}
	focus, _ := inv.value("--focus")
	cursor, _ := inv.value("--cursor")
	pattern, _ := inv.value("--pattern")
	request := survey.SummarizeRequest{Content: content, Task: focus, Cursor: cursor, Pattern: pattern, Observe: inv.pinOpened}
	if len(paths) == 1 {
		request.Path = paths[0]
	} else {
		request.Paths = paths
	}
	diskCtx := inv.tc
	diskCtx.Source.EditorDocuments = nil
	resp, _, err := inv.owners.Domain.Summarize.Summarize(inv.ctx, request, diskCtx)
	if err != nil {
		return err
	}
	resp.NextActions = slices.Clone(resp.NextActions)
	for i, action := range resp.NextActions {
		resp.NextActions[i], err = action.CommandFollowup()
		if err != nil {
			return fmt.Errorf("summarize follow-up: %w", err)
		}
	}
	return inv.structured(resp)
}

// runRepoMap runs one catalog bundle. Its probes read catalog and directory
// metadata only; it opens no file content to pin.
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
	bundle, hasBundle := inv.value("--bundle")
	if !hasBundle {
		bundle = repoMapBundle
	}
	diskCtx := inv.tc
	diskCtx.Source.EditorDocuments = nil
	resp, err := inv.owners.Domain.Repo.Survey(inv.ctx, reposurvey.RepoRequest{Bundle: bundle, Path: abs, LayoutDepth: depth}, diskCtx)
	if err != nil {
		return err
	}
	return inv.structured(resp)
}
