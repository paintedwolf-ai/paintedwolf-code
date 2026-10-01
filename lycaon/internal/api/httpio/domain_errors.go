package httpio

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ProjectLookupError answers a failed project lookup: an unknown id is 404,
// anything else a logged 500.
func (s *Responder) ProjectLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, project.ErrNotFound) {
		s.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
		return
	}
	s.InternalError(w, r, err)
}

// ProjectRegistryError maps project registry refusals to typed responses.
func (s *Responder) ProjectRegistryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		s.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
	case errors.Is(err, project.ErrDuplicateRoot):
		s.Fail(w, wire.ApiErrorCodeDuplicateRoot, "the folder is already attached to this project")
	case errors.Is(err, project.ErrDuplicateRootLabel):
		s.Fail(w, wire.ApiErrorCodeDuplicateRootLabel, "another folder in this project uses that label")
	case errors.Is(err, project.ErrRootNotFound):
		s.Fail(w, wire.ApiErrorCodeRootNotFound, "project root not found")
	case errors.Is(err, project.ErrDraftRootImmutable):
		s.Fail(w, wire.ApiErrorCodeDraftRootImmutable, "save the draft to a folder before changing its roots")
	case errors.Is(err, project.ErrRootBusy):
		s.Fail(w, wire.ApiErrorCodeRootBusy, "folder has in-flight dependents")
	case errors.Is(err, project.ErrProjectBusy):
		s.Fail(w, wire.ApiErrorCodeProjectBusy, "project has in-flight work")
	case errors.Is(err, project.ErrPromotionConflict), errors.Is(err, project.ErrPromotionPhase):
		s.Fail(w, wire.ApiErrorCodePromotionConflict, "the project is already being saved to a folder")
	case errors.Is(err, project.ErrPromotionNotFound):
		s.Fail(w, wire.ApiErrorCodePromotionNotFound, "project has no active save to folder")
	case errors.Is(err, project.ErrInvalidName), errors.Is(err, project.ErrInvalidRootLabel):
		s.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the name or label is not valid")
	case errors.Is(err, project.ErrRootRefused):
		s.RootRefused(w, err)
	case errors.Is(err, project.ErrInvalidPath), errors.Is(err, project.ErrPathNotFound), errors.Is(err, project.ErrNotDirectory),
		errors.Is(err, project.ErrPolicyDenied):
		s.PathError(w, r, err)
	default:
		s.InternalError(w, r, err)
	}
}

// PathError maps project path refusals to typed responses.
func (s *Responder) PathError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, project.ErrRootRefused):
		s.RootRefused(w, err)
	case errors.Is(err, project.ErrInvalidPath),
		errors.Is(err, project.ErrNotDirectory):
		s.Fail(w, wire.ApiErrorCodeInvalidPath, "the path is not a usable folder")
	case errors.Is(err, project.ErrPathNotFound):
		s.Fail(w, wire.ApiErrorCodePathNotFound, "project path not found")
	case errors.Is(err, project.ErrPolicyDenied):
		s.Fail(w, wire.ApiErrorCodeProjectPolicyDenied, "the host policy does not allow this folder")
	default:
		s.InternalError(w, r, err)
	}
}

// RootRefused maps structured root refusals to typed responses.
func (s *Responder) RootRefused(w http.ResponseWriter, err error) {
	code, _ := project.RootRefusedCode(err)
	switch code {
	case "write_root_is_filesystem_root":
		s.Fail(w, wire.ApiErrorCodeWriteRootIsFilesystemRoot, "the filesystem root cannot be a project folder")
	case "write_root_is_home":
		s.Fail(w, wire.ApiErrorCodeWriteRootIsHome, "the home folder cannot be a project folder")
	case "write_root_not_absolute":
		s.Fail(w, wire.ApiErrorCodeWriteRootNotAbsolute, "the folder path must be absolute")
	case "write_root_under_secret_store":
		s.Fail(w, wire.ApiErrorCodeWriteRootUnderSecretStore, "the folder is inside a credential store")
	default:
		s.Fail(w, wire.ApiErrorCodeInvalidPath, "the path is not a usable folder")
	}
}

// OverlayFormatError maps an overlay format refusal to its wire code.
// Reports whether it handled err.
func (s *Responder) OverlayFormatError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, settingsoverlay.ErrFormatTooNew):
		s.Fail(w, wire.ApiErrorCodeOverlayFormatUnsupported, "the project overlay format is newer than this app")
		return true
	case errors.Is(err, settingsoverlay.ErrFormatInvalid):
		s.Fail(w, wire.ApiErrorCodeOverlayFormatInvalid, "the project overlay format declaration is invalid")
		return true
	default:
		return false
	}
}
