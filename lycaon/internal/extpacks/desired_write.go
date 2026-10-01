package extpacks

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/fssync"
)

var extensionPathMu sync.Map

func lockExtensionPath(path string) func() {
	key := filepath.Clean(path)
	v, _ := extensionPathMu.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// EncodeDesired validates and canonically serializes one desired-state file.
// The subsystem owner commits the bytes.
func EncodeDesired(d DesiredState) ([]byte, error) {
	d.Format = DesiredFormat
	if d.Own == nil {
		d.Own = map[string]string{}
	}
	if err := ValidateDesired(d); err != nil {
		return nil, err
	}
	return yaml.Marshal(&d)
}

// syncExtensionDirectory makes a rename in a managed directory durable.
func syncExtensionDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}

// setPackRow upserts one pack row by id.
func setPackRow(d DesiredState, pack DesiredPack) DesiredState {
	for i := range d.Packs {
		if d.Packs[i].ID == pack.ID {
			d.Packs[i] = pack
			return d
		}
	}
	d.Packs = append(d.Packs, pack)
	return d
}

func dropPackFromDesired(d DesiredState, packID string) DesiredState {
	var packs []DesiredPack
	for _, p := range d.Packs {
		if p.ID != packID {
			packs = append(packs, p)
		}
	}
	d.Packs = packs
	own := map[string]string{}
	for k, v := range d.Own {
		if v != packID {
			own[k] = v
		}
	}
	d.Own = own
	return d
}

// DesiredMutation changes desired state.
type DesiredMutation struct {
	DisableUnit string // unit id to add to disabled
	EnableUnit  string // unit id to remove from disabled
	OwnUnit     string
	OwnPack     string
	SetPack     *DesiredPack // upsert pack row
	ClearOwn    string       // unit id
	// SetConfiguration replaces each named pack block; an empty block removes it.
	SetConfiguration map[string]map[string]any
	// UndeclinePacks clears recorded declines when suggestions are accepted.
	UndeclinePacks []string
}

// ApplyMutationToState is the pure desired-state mutation.
func ApplyMutationToState(d DesiredState, mut DesiredMutation) (DesiredState, error) {
	if mut.SetPack != nil {
		d = mergePackRow(d, *mut.SetPack)
	}
	if u := strings.TrimSpace(mut.DisableUnit); u != "" {
		d.Disabled = appendUnique(d.Disabled, u)
	}
	if u := strings.TrimSpace(mut.EnableUnit); u != "" {
		d.Disabled = removeString(d.Disabled, u)
	}
	if u := strings.TrimSpace(mut.ClearOwn); u != "" {
		delete(d.Own, u)
	}
	if u := strings.TrimSpace(mut.OwnUnit); u != "" {
		if d.Own == nil {
			d.Own = map[string]string{}
		}
		d.Own[u] = strings.TrimSpace(mut.OwnPack)
	}
	if configuration := mut.SetConfiguration; configuration != nil {
		if err := validateConfiguration(configuration); err != nil {
			return DesiredState{}, err
		}
		for packID, values := range configuration {
			if len(values) == 0 {
				delete(d.Configuration, packID)
				continue
			}
			if d.Configuration == nil {
				d.Configuration = map[string]map[string]any{}
			}
			cloned := make(map[string]any, len(values))
			for name, value := range values {
				cloned[name] = value
			}
			d.Configuration[packID] = cloned
		}
		if len(d.Configuration) == 0 {
			d.Configuration = nil
		}
	}
	for _, id := range mut.UndeclinePacks {
		d.Declined = removeString(d.Declined, strings.TrimSpace(id))
	}
	return d, nil
}

func mergePackRow(d DesiredState, patch DesiredPack) DesiredState {
	for i := range d.Packs {
		if d.Packs[i].ID != patch.ID {
			continue
		}
		if patch.Source != "" {
			d.Packs[i].Source = patch.Source
		}
		if patch.Version != "" {
			d.Packs[i].Version = patch.Version
			d.Packs[i].Ref = ""
			d.Packs[i].Development = false
		}
		if patch.Ref != "" {
			d.Packs[i].Ref = patch.Ref
			d.Packs[i].Version = ""
			d.Packs[i].Development = false
		}
		if patch.Development {
			d.Packs[i].Development = true
			d.Packs[i].Version = ""
			d.Packs[i].Ref = ""
		}
		if patch.Enabled != nil {
			d.Packs[i].Enabled = patch.Enabled
		}
		if patch.InstalledFrom != "" {
			d.Packs[i].InstalledFrom = patch.InstalledFrom
		}
		return d
	}
	d.Packs = append(d.Packs, patch)
	return d
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func removeString(list []string, v string) []string {
	var out []string
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
