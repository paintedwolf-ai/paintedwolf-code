package extpacks

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMetaPackNotFound is returned when a suite id is not discovered.
var ErrMetaPackNotFound = errors.New("meta-pack not found")

// ErrStockMetaPack is returned when an operation is refused for the stock suite.
var ErrStockMetaPack = errors.New("stock meta-pack")

// ErrMissingMembers prevents partial suite enablement.
var ErrMissingMembers = errors.New("meta-pack members missing")

// MetaMemberMutation is the pure suite enable/disable over desired state.
type MetaMemberMutation struct {
	MetaPackID string
	Members    []string
	Enable     bool
	Warnings   []string
	// Missing contains absent members accepted for disable operations.
	Missing []string
}

// PrepareMetaMemberMutation resolves a suite and its installed members.
func PrepareMetaMemberMutation(metaID string, enable bool) (MetaMemberMutation, error) {
	metaID = strings.TrimSpace(metaID)
	if metaID == "" {
		return MetaMemberMutation{}, fmt.Errorf("suite mutation: meta-pack id required")
	}
	meta, err := findDiscoveredMetaPack(metaID)
	if err != nil {
		return MetaMemberMutation{}, err
	}
	if !enable && IsStockMetaPackID(metaID) {
		// The stock suite must remain enabled.
		return MetaMemberMutation{}, fmt.Errorf(
			"%w: the stock suite is the app's own content and cannot be disabled — disable individual packs instead",
			ErrStockMetaPack)
	}
	inventory, err := inventoryPackIDSet()
	if err != nil {
		return MetaMemberMutation{}, err
	}
	mutation := MetaMemberMutation{MetaPackID: metaID, Enable: enable}
	var missing []string
	for _, id := range meta.Manifest.Members {
		if !inventory[id] {
			missing = append(missing, id)
			continue
		}
		mutation.Members = append(mutation.Members, id)
	}
	if len(missing) > 0 {
		if enable {
			return MetaMemberMutation{}, fmt.Errorf("%w: %s", ErrMissingMembers, strings.Join(missing, ", "))
		}
		for _, id := range missing {
			mutation.Warnings = append(mutation.Warnings, fmt.Sprintf("%s: member pack %q is missing", DiagMemberMissing, id))
		}
		mutation.Missing = missing
	}
	return mutation, nil
}

// ApplyTo updates present members and removes absent flag-only rows.
func (m MetaMemberMutation) ApplyTo(d DesiredState) DesiredState {
	enabled := m.Enable
	for _, id := range m.Members {
		v := enabled
		d = upsertPackEnabled(d, id, &v)
	}
	for _, id := range m.Missing {
		d = dropStaleEnabledOnlyRow(d, id)
	}
	return d
}

// dropStaleEnabledOnlyRow removes a row without installation identity.
func dropStaleEnabledOnlyRow(d DesiredState, packID string) DesiredState {
	row, ok := DesiredPackRow(d, packID)
	if !ok {
		return d
	}
	if row.Source != "" || row.Ref != "" || row.Development || row.InstalledFrom != "" {
		return d
	}
	return dropPackFromDesired(d, packID)
}

func findDiscoveredMetaPack(metaID string) (MetaPack, error) {
	metas, _, err := DiscoverMetaPacks()
	if err != nil {
		return MetaPack{}, err
	}
	for _, m := range metas {
		if m.Manifest.ID == metaID {
			return m, nil
		}
	}
	return MetaPack{}, fmt.Errorf("%w: %s", ErrMetaPackNotFound, metaID)
}

func inventoryPackIDSet() (map[string]bool, error) {
	content, err := DiscoverAllContent(nil)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, pc := range content {
		out[pc.Pack.ID] = true
	}
	return out, nil
}
