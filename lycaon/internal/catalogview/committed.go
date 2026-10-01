package catalogview

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// ForCommitted omits invalid non-stock packs and records diagnostics; stock failures are fatal.
func (c *Cache) ForCommitted(ctx context.Context, eff *extpacks.EffectiveCatalog) (*View, *extpacks.EffectiveCatalog, error) {
	return c.isolate(ctx, eff, nil)
}

// ForCandidate requires protected packs to compile and isolates other invalid packs.
func (c *Cache) ForCandidate(
	ctx context.Context,
	eff *extpacks.EffectiveCatalog,
	protected []string,
) (*View, *extpacks.EffectiveCatalog, error) {
	keep := make(map[string]bool, len(protected))
	for _, id := range protected {
		if id != "" {
			keep[id] = true
		}
	}
	return c.isolate(ctx, eff, keep)
}

func (c *Cache) isolate(
	ctx context.Context,
	eff *extpacks.EffectiveCatalog,
	protected map[string]bool,
) (*View, *extpacks.EffectiveCatalog, error) {
	view, err := c.For(ctx, eff)
	if err == nil {
		return view, eff, nil
	}

	omitted := map[string]string{}
	curErr := err
	// Typed attribution first: contribution faults name their packs. The
	// omitted set only grows, so pack count bounds the loop.
	for i := 0; i <= len(eff.Packs); i++ {
		reasons, fatal := attributedFaults(eff, curErr, protected)
		if fatal || len(reasons) == 0 {
			break
		}
		for id, why := range reasons {
			omitted[id] = why
		}
		next, ok := eff.WithOmitted(ctx, omitted)
		if !ok {
			return nil, nil, err
		}
		view, curErr = c.For(ctx, next)
		if curErr == nil {
			return view, next, nil
		}
	}
	return c.readmit(ctx, eff, omitted, protected, err)
}

// attributedFaults maps compile faults to omit reasons. A bundled or protected
// pack fault is fatal.
func attributedFaults(
	eff *extpacks.EffectiveCatalog,
	err error,
	protected map[string]bool,
) (reasons map[string]string, fatal bool) {
	var compileErr *contribution.CompileError
	if !errors.As(err, &compileErr) {
		return nil, false
	}
	reasons = map[string]string{}
	for _, fault := range compileErr.Faults {
		if eff.StockAuthority(fault.PackID) || protected[fault.PackID] {
			return nil, true
		}
		msg := fault.Error()
		if prior, ok := reasons[fault.PackID]; ok {
			msg = prior + "; " + fault.Error()
		}
		reasons[fault.PackID] = msg
	}
	return reasons, false
}

// readmit adds packs in sorted order to isolate unattributed compilation failures.
func (c *Cache) readmit(
	ctx context.Context,
	eff *extpacks.EffectiveCatalog,
	omitted map[string]string,
	protected map[string]bool,
	origErr error,
) (*View, *extpacks.EffectiveCatalog, error) {
	var candidates []string
	for _, id := range eff.ContributingPackIDs() {
		if _, held := omitted[id]; held || eff.StockAuthority(id) {
			continue
		}
		candidates = append(candidates, id)
	}
	if len(candidates) == 0 {
		return nil, nil, origErr
	}
	sort.Strings(candidates)

	const holdReason = "held while isolating a committed-state compile failure"
	// Trial compilations bypass the cache to preserve published views.
	trial := func(keep map[string]string, admitted map[string]bool, admitting string) error {
		reasons := map[string]string{}
		for id, why := range keep {
			reasons[id] = why
		}
		for _, id := range candidates {
			if id != admitting && !admitted[id] {
				if _, held := reasons[id]; !held {
					reasons[id] = holdReason
				}
			}
		}
		next, ok := eff.WithOmitted(ctx, reasons)
		if !ok {
			return origErr
		}
		_, err := Build(ctx, c.moduleRoot, next)
		return err
	}

	// Stock and protected packs form the required compilation baseline.
	floorAdmitted := map[string]bool{}
	for _, id := range candidates {
		if protected[id] {
			floorAdmitted[id] = true
		}
	}
	if err := trial(omitted, floorAdmitted, ""); err != nil {
		return nil, nil, origErr
	}

	keep := map[string]string{}
	for id, why := range omitted {
		keep[id] = why
	}
	admitted := floorAdmitted
	for _, id := range candidates {
		if admitted[id] {
			continue
		}
		if err := trial(keep, admitted, id); err != nil {
			keep[id] = fmt.Sprintf("pack does not compile: %v", err)
			continue
		}
		admitted[id] = true
	}

	final, ok := eff.WithOmitted(ctx, keep)
	if !ok {
		return nil, nil, origErr
	}
	view, err := c.For(ctx, final)
	if err != nil {
		// Packs that compile alone but not together cannot be isolated per pack.
		return nil, nil, origErr
	}
	return view, final, nil
}

// InForce reports the isolated catalog, retaining the resolved catalog on fatal failure.
func (c *Cache) InForce(ctx context.Context, eff *extpacks.EffectiveCatalog) *extpacks.EffectiveCatalog {
	if c == nil || eff == nil {
		return eff
	}
	_, committed, err := c.ForCommitted(ctx, eff)
	if err != nil || committed == nil {
		return eff
	}
	return committed
}
