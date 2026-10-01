package promptloop

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

// projectGitDiff retains the screened observation before fitting its model view.
func projectGitDiff(hostDir string, content tooloutput.ScreenedOutput, maxSpillBytes int) (string, *tools.ToolReject) {
	var page git.DiffToolResponse
	if err := json.Unmarshal([]byte(content.String()), &page); err != nil || !page.Available || page.Stat || page.MaxBytes <= 0 {
		return content.String(), nil //nolint:nilerr // output that is not a diff page passes through unchanged
	}
	size := 0
	for _, file := range page.Files {
		size += len(file.Diff)
	}
	if size <= page.MaxBytes {
		return content.String(), nil
	}
	full := tooloutput.SpillWholeToolOutput(hostDir, content, maxSpillBytes)
	if full.SpillPath == "" || full.SpillCapped {
		return "", gitSpillReject(full)
	}
	page.WireSpillPath = full.SpillPath
	// A raw diff spill supports ordinary read offset/limit without JSON-escaped hunks.
	if len(page.Files) > 0 && len(page.Files[0].Diff) > page.MaxBytes {
		if reject := retainDiffHunks(hostDir, &page.Files[0], maxSpillBytes); reject != nil {
			return "", reject
		}
	}
	kept, cut := git.FitDiffPage(page.Files, page.MaxBytes)
	page.Files = kept
	if cut {
		page.FilesTruncated = true
		next := page.Offset + len(kept)
		page.NextOffset = &next
	}
	out, err := git.MarshalDiffToolResponse(page, nil)
	if err != nil {
		return content.String(), nil //nolint:nilerr // an unencodable page keeps the screened output
	}
	return out, nil
}

func retainDiffHunks(hostDir string, file *git.DiffToolEntry, maxSpillBytes int) *tools.ToolReject {
	diff := file.Diff
	file.DiffLines = strings.Count(diff, "\n")
	if !strings.HasSuffix(diff, "\n") {
		file.DiffLines++
	}
	spill := tooloutput.SpillWholeRaw(hostDir, tooloutput.Screened(diff), maxSpillBytes)
	if spill.SpillPath == "" || spill.SpillCapped {
		return gitSpillReject(spill)
	}
	file.DiffSpillPath = spill.SpillPath
	return nil
}

func gitSpillReject(out tooloutput.WireSpillOutcome) *tools.ToolReject {
	if out.RejectCode != "" {
		return &tools.ToolReject{Code: out.RejectCode, Data: out.RejectData}
	}
	return &tools.ToolReject{Code: tooloutput.ToolOutputSpillUnavailableCode, Data: map[string]any{
		"tool": "git_diff", "bytes": out.OriginalBytes, "reason": "spill_unavailable",
	}}
}
