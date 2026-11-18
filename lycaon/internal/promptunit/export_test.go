package promptunit

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
