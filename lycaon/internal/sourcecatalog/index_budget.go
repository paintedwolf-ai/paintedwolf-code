package sourcecatalog

import (
	"context"
	"database/sql"
	"errors"
	"path"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// indexCut is where a budget fell during one batch: the directory to leave
// unobserved, and the bound that refused it. A zero cut means every bound held.
type indexCut struct {
	Dir    string
	Reason sandbox.BoundaryReason
}

func (c indexCut) fell() bool { return c.Reason != "" }

// indexBudget charges each listing to its directory and ancestors.
// The walk limit takes precedence, followed by the shallowest exceeded subtree.
type indexBudget struct {
	limits sandbox.SurveyBudgets
}

// listingLimit is how many entries a listing may read before the directory is
// known to exceed DirectoryEntries: the cap plus one. Zero means unbounded.
func (b indexBudget) listingLimit() int {
	if b.limits.DirectoryEntries <= 0 {
		return 0
	}
	return b.limits.DirectoryEntries + 1
}

// observationLimit stops metadata discovery as soon as this consumer can decide
// that a directory or one of its ancestors exceeds its remaining budget.
func (b indexBudget) observationLimit(ctx context.Context, tx *sql.Tx, dir string) (int, error) {
	limit := b.listingLimit()
	bound := func(remaining int) {
		n := max(1, remaining+1)
		if limit == 0 || n < limit {
			limit = n
		}
	}
	if b.limits.WalkEntries > 0 {
		var observed int
		if err := tx.QueryRowContext(ctx, "SELECT observed FROM totals WHERE id=1").Scan(&observed); err != nil {
			return 0, err
		}
		bound(b.limits.WalkEntries - observed)
	}
	if b.limits.SubtreeEntries > 0 {
		for _, ancestor := range indexAncestors(dir) {
			if ancestor == "." {
				continue
			}
			var observed int
			err := tx.QueryRowContext(ctx, "SELECT observed FROM charges WHERE path=?", ancestor).Scan(&observed)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			bound(b.limits.SubtreeEntries - observed)
		}
	}
	return limit, nil
}

// reset clears the accounting before a full pass. A pass over changed
// subtrees is bounded from those subtrees.
func (b indexBudget) reset(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM charges"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE totals SET observed=0 WHERE id=1")
	return err
}

// charge records n entries observed while listing dir and reports the bound
// that fell, if any.
func (b indexBudget) charge(ctx context.Context, tx *sql.Tx, dir string, n int) (indexCut, error) {
	if n <= 0 {
		return indexCut{}, nil
	}
	var observed int
	err := tx.QueryRowContext(ctx, "UPDATE totals SET observed=observed+? WHERE id=1 RETURNING observed", n).Scan(&observed)
	if err != nil {
		return indexCut{}, err
	}
	ancestors := indexAncestors(dir)
	charged := make([]int, len(ancestors))
	for i, ancestor := range ancestors {
		err = tx.QueryRowContext(ctx,
			`INSERT INTO charges(path,observed) VALUES(?,?)
			 ON CONFLICT(path) DO UPDATE SET observed=observed+excluded.observed RETURNING observed`,
			ancestor, n).Scan(&charged[i])
		if err != nil {
			return indexCut{}, err
		}
	}
	if b.limits.WalkEntries > 0 && observed > b.limits.WalkEntries {
		return indexCut{Dir: ".", Reason: sandbox.BoundaryWalkBudget}, nil
	}
	if b.limits.SubtreeEntries <= 0 {
		return indexCut{}, nil
	}
	// The root's subtree is the walk, which WalkEntries already bounds.
	for i, ancestor := range ancestors {
		if ancestor != "." && charged[i] > b.limits.SubtreeEntries {
			return indexCut{Dir: ancestor, Reason: sandbox.BoundarySubtreeCap}, nil
		}
	}
	return indexCut{}, nil
}

// indexAncestors lists rel and every directory above it, shallowest first, so a
// cut is reported at the highest directory whose subtree is over its bound.
func indexAncestors(rel string) []string {
	var chain []string
	for current := rel; ; current = path.Dir(current) {
		chain = append(chain, current)
		if current == "." {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}
