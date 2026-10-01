package harnessfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
)

type WriteResourceRequest struct {
	Capture string `json:"capture"`
	Project string `json:"project"`
}

type WriteResource struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Capture string `json:"capture"`
}

// PrepareWriteResource records ownership before creating an external output directory.
func PrepareWriteResource(request WriteResourceRequest) (WriteResource, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return WriteResource{}, err
	}
	return allocateWriteResource(request, filepath.Join(home, "shared-output"),
		confine.WriteRootsForProject("", []string{request.Project}))
}

func allocateWriteResource(request WriteResourceRequest, base string, writeRoots []string) (WriteResource, error) {
	var resource WriteResource
	if !filepath.IsAbs(request.Capture) || !filepath.IsAbs(request.Project) || !filepath.IsAbs(base) {
		return resource, fmt.Errorf("capture, project, and output parent must be absolute paths")
	}
	resource = WriteResource{ID: uuid.NewString(), Capture: request.Capture}
	resource.Path = filepath.Join(base, resource.ID)
	if confine.PathWithinWriteRoots(resource.Path, writeRoots) {
		return resource, fmt.Errorf("external output lies within a default write root")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return resource, err
	}
	info, err := os.Lstat(base)
	if err != nil {
		return resource, err
	}
	if !info.IsDir() {
		return resource, fmt.Errorf("external output parent is not a directory")
	}
	if _, err := os.Lstat(resource.Path); !os.IsNotExist(err) {
		return resource, fmt.Errorf("external output identity is not available")
	}
	receipts := filepath.Join(request.Capture, "external-resources")
	if err := os.MkdirAll(receipts, 0o700); err != nil {
		return resource, err
	}
	body, err := json.Marshal(resource)
	if err != nil {
		return resource, err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: receipts, Rel: resource.ID + ".json"}, Source: bytes.NewReader(body), Mode: 0o600})
	if err != nil {
		return resource, err
	}
	if err := os.Mkdir(resource.Path, 0o700); err != nil {
		return resource, err
	}
	return resource, nil
}
