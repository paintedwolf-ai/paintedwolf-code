package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// RepoRef groups project roots by repository.
type RepoRef struct {
	ID        string // derived; empty when Available is false
	Toplevel  string // canonical absolute path; empty when Available is false
	Label     string
	RootIDs   []string // project root ids, in project-roots order
	Available bool
}

// DeriveRepoID hashes the canonical repository root.
func DeriveRepoID(toplevel string) string {
	top := gitrepo.CanonicalDir(toplevel)
	if top == "" || top == "." {
		return ""
	}
	sum := sha256.Sum256([]byte(top))
	return "r" + hex.EncodeToString(sum[:8])
}

// LabelFor returns the primary-most member label.
func LabelFor(members []projectroot.RootRef, toplevel string) string {
	chosen, ok := primaryMost(members)
	if ok && chosen.Label != "" {
		return chosen.Label
	}
	base := filepath.Base(toplevel)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return toplevel
	}
	return base
}

// DiscoverRepos groups roots by containing repository.
func DiscoverRepos(ctx context.Context, roots []projectroot.RootRef) []RepoRef {
	if len(roots) == 0 {
		return nil
	}

	type group struct {
		toplevel string
		members  []projectroot.RootRef
	}
	byTop := make(map[string]*group)
	var topOrder []string
	var nonRepos []RepoRef

	for _, root := range roots {
		top, ok := probeToplevel(ctx, root.Path)
		if !ok {
			nonRepos = append(nonRepos, RepoRef{
				RootIDs:   []string{root.ID},
				Label:     labelForRoot(root),
				Available: false,
			})
			continue
		}
		g, exists := byTop[top]
		if !exists {
			g = &group{toplevel: top}
			byTop[top] = g
			topOrder = append(topOrder, top)
		}
		g.members = append(g.members, root)
	}

	repos := make([]RepoRef, 0, len(topOrder)+len(nonRepos))
	for _, top := range topOrder {
		g := byTop[top]
		ids := make([]string, 0, len(g.members))
		for _, m := range g.members {
			ids = append(ids, m.ID)
		}
		repos = append(repos, RepoRef{
			ID:        DeriveRepoID(top),
			Toplevel:  top,
			Label:     LabelFor(g.members, top),
			RootIDs:   ids,
			Available: true,
		})
	}
	repos = append(repos, nonRepos...)

	primaryID := ""
	if p, err := projectroot.PrimaryRoot(roots); err == nil {
		primaryID = p.ID
	}
	return OrderRepos(repos, "", primaryID)
}

// OrderRepos prioritizes active and primary roots.
func OrderRepos(repos []RepoRef, activeRootID, primaryRootID string) []RepoRef {
	if len(repos) == 0 {
		return nil
	}
	out := append([]RepoRef(nil), repos...)
	activeRootID = strings.TrimSpace(activeRootID)
	primaryRootID = strings.TrimSpace(primaryRootID)

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, rb := orderRank(a, activeRootID, primaryRootID), orderRank(b, activeRootID, primaryRootID)
		if ra != rb {
			return ra < rb
		}
		if a.Label != b.Label {
			return a.Label < b.Label
		}
		if a.Toplevel != b.Toplevel {
			return a.Toplevel < b.Toplevel
		}
		return rootIDsKey(a.RootIDs) < rootIDsKey(b.RootIDs)
	})
	return out
}

func probeToplevel(ctx context.Context, rootPath string) (string, bool) {
	if strings.TrimSpace(rootPath) == "" {
		return "", false
	}
	out, code, err := gitexec.Run(ctx, rootPath, []string{"rev-parse", "--show-toplevel"}, hermeticOpts(0))
	if err != nil || code != 0 {
		return "", false
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", false
	}
	return gitrepo.CanonicalDir(top), true
}

func labelForRoot(root projectroot.RootRef) string {
	if root.Label != "" {
		return root.Label
	}
	base := filepath.Base(root.Path)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return root.Path
	}
	return base
}

func primaryMost(members []projectroot.RootRef) (projectroot.RootRef, bool) {
	if len(members) == 0 {
		return projectroot.RootRef{}, false
	}
	for _, m := range members {
		if m.IsPrimary {
			return m, true
		}
	}
	return members[0], true
}

func orderRank(r RepoRef, activeRootID, primaryRootID string) int {
	if rootIDsContain(r.RootIDs, activeRootID) {
		return 0
	}
	if rootIDsContain(r.RootIDs, primaryRootID) {
		return 1
	}
	if r.Available {
		return 2
	}
	return 3
}

func rootIDsContain(ids []string, want string) bool {
	if want == "" {
		return false
	}
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func rootIDsKey(ids []string) string {
	return strings.Join(ids, "\x00")
}

func cloneRepoRefs(in []RepoRef) []RepoRef {
	if in == nil {
		return nil
	}
	out := make([]RepoRef, len(in))
	for i, r := range in {
		out[i] = RepoRef{
			ID:        r.ID,
			Toplevel:  r.Toplevel,
			Label:     r.Label,
			RootIDs:   append([]string(nil), r.RootIDs...),
			Available: r.Available,
		}
	}
	return out
}
