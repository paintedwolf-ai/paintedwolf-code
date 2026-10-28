package fseffect

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
)

var ErrDirectoryNotEmpty = errors.New("directory is not empty")

type EmptyParent struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
	Mode     uint32 `json:"mode"`
}

// EmptyParents records the ancestors a leaf removal may leave empty. The stop
// directory is retained; identities prevent pruning a replacement directory.
type EmptyParents struct {
	Source       string        `json:"source"`
	Stop         string        `json:"stop"`
	StopIdentity string        `json:"stop_identity"`
	Parents      []EmptyParent `json:"parents"`
}

func PrepareEmptyParents(loc Location, stop string) (*EmptyParents, error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return nil, err
	}
	plan := &EmptyParents{Source: filepath.ToSlash(clean.Rel), Stop: filepath.ToSlash(filepath.Clean(stop))}
	chain, err := emptyParentChain(clean.Root, plan)
	if err != nil {
		return nil, err
	}
	plan.StopIdentity, err = fspath.EntryIdentity(filepath.Join(clean.Root, filepath.FromSlash(plan.Stop)))
	if err != nil {
		return nil, err
	}
	for _, rel := range chain {
		abs := filepath.Join(clean.Root, rel)
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, ErrSymlink
		}
		identity, err := fspath.EntryIdentity(abs)
		if err != nil {
			return nil, err
		}
		plan.Parents = append(plan.Parents, EmptyParent{Path: filepath.ToSlash(rel), Identity: identity, Mode: uint32(info.Mode().Perm())})
	}
	return plan, nil
}

func emptyParentChain(root string, plan *EmptyParents) ([]string, error) {
	if plan == nil || plan.Stop == "" || filepath.IsAbs(plan.Stop) {
		return nil, ErrInvalidPath
	}
	source, err := cleanLocation(Location{Root: root, Rel: plan.Source})
	if err != nil {
		return nil, err
	}
	stop := filepath.Clean(filepath.FromSlash(plan.Stop))
	if stop != "." {
		if _, err := cleanLocation(Location{Root: root, Rel: stop}); err != nil {
			return nil, err
		}
	}
	var chain []string
	for parent := filepath.Dir(source.Rel); parent != stop; parent = filepath.Dir(parent) {
		if parent == "." {
			return nil, ErrInvalidPath
		}
		chain = append(chain, parent)
	}
	return chain, nil
}

func ValidateEmptyParents(root string, plan *EmptyParents) error {
	chain, err := emptyParentChain(root, plan)
	if err != nil {
		return err
	}
	if plan.StopIdentity == "" || len(chain) != len(plan.Parents) {
		return ErrInvalidPath
	}
	for i, rel := range chain {
		if filepath.ToSlash(rel) != plan.Parents[i].Path || plan.Parents[i].Identity == "" || plan.Parents[i].Mode&^0777 != 0 {
			return ErrInvalidPath
		}
	}
	return nil
}

// PruneEmptyParents is idempotent after a journaled leaf removal. A populated
// or replaced ancestor stops cleanup without removing any of its contents.
func PruneEmptyParents(ctx context.Context, root string, plan *EmptyParents) error {
	if plan == nil {
		return nil
	}
	if err := ValidateEmptyParents(root, plan); err != nil {
		return err
	}
	stopID, err := fspath.EntryIdentity(filepath.Join(root, filepath.FromSlash(plan.Stop)))
	if err != nil {
		return err
	}
	if stopID != plan.StopIdentity {
		return ErrPostcondition
	}
	for _, parent := range plan.Parents {
		if err := ctx.Err(); err != nil {
			return err
		}
		loc := Location{Root: root, Rel: filepath.FromSlash(parent.Path)}
		id, err := fspath.EntryIdentity(filepath.Join(root, loc.Rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if id != parent.Identity {
			return nil
		}
		if err := RemoveEmptyDirectoryGuarded(loc, parent.Identity); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if errors.Is(err, ErrDirectoryNotEmpty) {
				return nil
			}
			return err
		}
	}
	return nil
}
