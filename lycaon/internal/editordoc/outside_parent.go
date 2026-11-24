package editordoc

import (
	"context"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
)

func outsideParentBinding(file, identity string) string {
	if identity == "" {
		return ""
	}
	return path.Dir(file) + "\x00" + identity
}

func (d *Document) checkObservedParent(identity string) error {
	parent, expected, ok := strings.Cut(d.OutsideParentBinding, "\x00")
	if identity != "" && ok && parent == path.Dir(d.Path) && identity != expected {
		return projectsource.ErrSourceWriteConflict
	}
	return nil
}

func (o documentOpener) bindObservedParent(ctx context.Context, d *Document, identity string) error {
	if err := d.checkObservedParent(identity); err != nil {
		return err
	}
	binding := outsideParentBinding(d.Path, identity)
	if binding == d.OutsideParentBinding {
		return nil
	}
	_, err := o.s.store.db.ExecContext(ctx, `UPDATE editor_documents SET outside_parent_binding=? WHERE id=?`, binding, d.ID)
	if err == nil {
		d.OutsideParentBinding = binding
	}
	return err
}

func (o documentOpener) publicationParent(ctx context.Context, root string, d *Document) (string, error) {
	if root != projectroot.OutsidePath {
		return "", nil
	}
	identity, err := projectroot.OutsideDirectoryIdentity(projectroot.OutsideAbs(path.Dir(d.Path)))
	if err != nil {
		return "", projectsource.ErrSourceWriteConflict
	}
	// A host retarget changes the path before the next save.
	if err := o.bindObservedParent(ctx, d, identity); err != nil {
		return "", err
	}
	return identity, nil
}
