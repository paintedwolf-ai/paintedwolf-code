package promptunit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// Catalog is the validated set of units in the effective catalog.
type Catalog struct {
	units    []Unit
	byID     map[string]Unit
	revision string
	// Rejected lists units from non-stock packs whose front matter did not
	// validate; they are left out rather than failing the catalog.
	Rejected []Rejection
}

// Rejection records one unit the catalog refused.
type Rejection struct {
	UnitID string
	PackID string
	Reason string
}

// Load reads every shared/units/*.md unit from the effective catalog. A stock
// unit that fails validation is an error; a non-stock one is recorded under
// Rejected and skipped.
func Load(eff *extpacks.EffectiveCatalog) (Catalog, error) {
	if eff == nil {
		return Catalog{}, errors.New("promptunit: effective catalog required")
	}
	cat := Catalog{byID: map[string]Unit{}}
	var faults []string
	for _, id := range eff.LoadedUnitIDs() {
		if !strings.HasPrefix(id, UnitIDPrefix) {
			continue
		}
		stem := strings.TrimPrefix(id, UnitIDPrefix)
		if strings.Contains(stem, "/") {
			continue
		}
		content, packID, ok := eff.UnitContent(id)
		if !ok {
			continue
		}
		stock := eff.StockAuthority(packID)
		unit, err := Parse(stem, content)
		if err != nil {
			if stock {
				faults = append(faults, err.Error())
			} else {
				cat.Rejected = append(cat.Rejected, Rejection{UnitID: id, PackID: packID, Reason: err.Error()})
			}
			continue
		}
		unit.PackID = packID
		unit.Stock = stock
		cat.units = append(cat.units, unit)
		cat.byID[unit.ID] = unit
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return Catalog{}, fmt.Errorf("promptunit: %s", strings.Join(faults, "; "))
	}
	sortUnits(cat.units)
	cat.revision = revisionOf(cat.units)
	return cat, nil
}

var cached struct {
	mu       sync.Mutex
	revision string
	catalog  Catalog
	err      error
}

// LoadCached loads the catalog for eff once per effective revision.
func LoadCached(eff *extpacks.EffectiveCatalog) (Catalog, error) {
	if eff == nil {
		return Catalog{}, errors.New("promptunit: effective catalog required")
	}
	cached.mu.Lock()
	defer cached.mu.Unlock()
	if cached.revision == eff.Revision && eff.Revision != "" {
		return cached.catalog, cached.err
	}
	cached.catalog, cached.err = Load(eff)
	cached.revision = eff.Revision
	return cached.catalog, cached.err
}

// FromUnits builds a catalog from already-validated units (tests and tools).
func FromUnits(units []Unit) Catalog {
	cat := Catalog{byID: make(map[string]Unit, len(units))}
	for _, u := range units {
		if u.UnitID == "" {
			u.UnitID = UnitIDPrefix + u.ID
		}
		if u.Ref == "" {
			u.Ref = RefPrefix + u.ID
		}
		if len(u.Hosts) == 0 {
			u.Hosts = []Host{HostCoordinator}
		}
		if u.Order == 0 {
			u.Order = DefaultOrder
		}
		cat.units = append(cat.units, u)
		cat.byID[u.ID] = u
	}
	sortUnits(cat.units)
	cat.revision = revisionOf(cat.units)
	return cat
}

func revisionOf(units []Unit) string {
	h := sha256.New()
	for _, u := range units {
		_, _ = h.Write([]byte(u.ID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(u.Description))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(u.Slot))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strings.Join(u.Attaches, ",")))
		_, _ = h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Revision identifies the unit set and descriptions for receipts.
func (c Catalog) Revision() string { return c.revision }

// Units returns every unit in render order.
func (c Catalog) Units() []Unit { return append([]Unit(nil), c.units...) }

// Unit returns one unit by stem.
func (c Catalog) Unit(id string) (Unit, bool) {
	u, ok := c.byID[strings.TrimSpace(id)]
	return u, ok
}

// Len returns the number of units.
func (c Catalog) Len() int { return len(c.units) }

// Selection is what one prompt render knows about its turn.
type Selection struct {
	Host Host
	// Mode is the execution mode family; empty matches units without modes.
	Mode string
	// Offered is every tool on this call: floor plus loaded.
	Offered map[string]bool
	// Floor is the subset of Offered a surface carries on every call.
	Floor map[string]bool
	// Omitted names units the turn decision left out with confidence.
	Omitted map[string]bool
}

// applies reports whether a unit belongs to this render before omissions.
func (c Catalog) applies(u Unit, sel Selection) bool {
	if !u.HostsFor(sel.Host) || !u.ModeFits(sel.Mode) {
		return false
	}
	if len(u.Attaches) == 0 {
		return true
	}
	return u.AttachedTo(sel.Offered)
}

// Rendered returns the units each slot renders for sel, in order.
func (c Catalog) Rendered(sel Selection) map[Slot][]Unit {
	out := make(map[Slot][]Unit, len(Slots()))
	for _, u := range c.units {
		if !c.applies(u, sel) || sel.Omitted[u.ID] {
			continue
		}
		out[u.Slot] = append(out[u.Slot], u)
	}
	return out
}

// Candidates returns the units the turn decision scores: those that apply to
// the render and do not simply follow a loadable tool. Offered here is the
// widest set the turn could offer (floor plus every loadable tool), so a unit
// attached only to loadable tools is excluded as following them.
func (c Catalog) Candidates(host Host, mode string, floor, loadable map[string]bool) []Unit {
	widest := make(map[string]bool, len(floor)+len(loadable))
	for name := range floor {
		widest[name] = true
	}
	for name := range loadable {
		widest[name] = true
	}
	sel := Selection{Host: host, Mode: mode, Offered: widest, Floor: floor}
	var out []Unit
	for _, u := range c.units {
		if !c.applies(u, sel) || u.Follows(floor) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// Fingerprint identifies a rendered selection for prompt-cache keys.
func Fingerprint(rendered map[Slot][]Unit) string {
	var ids []string
	for _, units := range rendered {
		for _, u := range units {
			ids = append(ids, u.ID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
