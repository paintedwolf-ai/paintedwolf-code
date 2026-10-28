package native

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

// Git file headers precede hunks; source lines cannot supply path attribution.
func deliverGitPatchCredentials(ctx context.Context, tc tools.ToolContext, patch string) error {
	for _, block := range git.SplitDiffBlocks(patch) {
		seen := map[string]bool{}
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "@@") {
				break
			}
			if !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") {
				continue
			}
			path := strings.TrimSpace(line[4:])
			if strings.HasPrefix(path, "\"") {
				decoded, err := strconv.Unquote(path)
				if err != nil {
					continue
				}
				path = decoded
			}
			if path == "/dev/null" {
				continue
			}
			if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
				path = path[2:]
			}
			if path == "" || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || seen[path] {
				continue
			}
			seen[path] = true
			abs, _, err := projectroot.ResolveAbs(tc.Source.Roots, tc.Source.ActiveRootID, path)
			if err != nil {
				return err
			}
			if err := tc.ObserveCredentialDelivery(ctx, abs, path, block); err != nil {
				return err
			}
		}
	}
	return nil
}
