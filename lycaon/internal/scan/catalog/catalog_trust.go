package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// Catalog trust RejectedRow codes (closed set).
const (
	RejectProjectFieldsForbidden = "project_fields_forbidden"
	RejectProjectUnknownID       = "project_unknown_id"
	// RejectProjectUnreadable: the whole overlay failed to read or parse —
	// distinct from one bad row. Spelled like detectionpack's code so one
	// vocabulary covers both overlays.
	RejectProjectUnreadable        = "project_unreadable"
	RejectInvalidEntry             = "invalid_entry"
	RejectDuplicateID              = "duplicate_id"
	RejectCommunityDriverForbidden = "community_driver_forbidden"
	RejectCommunityParserForbidden = "community_parser_forbidden"
	RejectArgvProjectPathForbidden = "argv_project_path_forbidden"
	RejectArgvShellForbidden       = "argv_shell_forbidden"
	RejectMapperMissing            = "mapper_missing"
	RejectBinaryMissing            = "binary_missing"
	RejectSlotConflict             = "slot_conflict"
)

// CatalogSource is which layer last authored the effective row (or last enable writer).
type CatalogSource string

const (
	CatalogSourceHost    CatalogSource = "host"
	CatalogSourceUser    CatalogSource = "user"
	CatalogSourceProject CatalogSource = "project"
)

// RejectedRow is a catalog entry (or field set) refused by trust gates.
type RejectedRow struct {
	ID     string
	Code   string
	Detail string
}

// ResolveBinaryOutsideRoots returns a canonical executable outside workspace roots.
func ResolveBinaryOutsideRoots(command0 string, workspaceRoots []string) (string, error) {
	if err := AssertBinaryOutsideRoots(command0, workspaceRoots); err != nil {
		return "", err
	}
	resolved, err := exec.LookPath(command0)
	if err != nil {
		return "", storeErr(RejectBinaryMissing, fmt.Sprintf("resolve command[0] %q: %v", command0, err))
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", storeErr(RejectBinaryMissing, fmt.Sprintf("canonicalize command[0] %q: %v", command0, err))
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", storeErr(RejectInvalidEntry, fmt.Sprintf("absolute command[0] %q: %v", command0, err))
	}
	if err := assertPathOutsideRoots(resolved, workspaceRoots); err != nil {
		return "", err
	}
	return resolved, nil
}

// MergeOptions configures MergeScannerCatalog.
type MergeOptions struct {
	// WorkspaceRoots are used for argv path checks; primary root minimum.
	WorkspaceRoots []string
	// UserConfigDir resolves device mapper files.
	UserConfigDir string
}

// ValidateExternalArgv checks an external command vector.
func ValidateExternalArgv(command []string, workspaceRoots []string) error {
	if len(command) == 0 {
		return storeErr(RejectInvalidEntry, "empty command")
	}
	for i, arg := range command {
		if strings.TrimSpace(arg) == "" {
			return storeErr(RejectInvalidEntry, fmt.Sprintf("empty command argument at index %d", i))
		}
		if containsShellMetachars(arg) {
			return storeErr(RejectArgvShellForbidden, fmt.Sprintf("command argument %q contains shell metacharacters", arg))
		}
	}
	if err := AssertBinaryOutsideRoots(command[0], workspaceRoots); err != nil {
		return err
	}
	// Host tokens expand during execution.
	for i := 1; i < len(command); i++ {
		arg := command[i]
		if strings.Contains(arg, ArgTokenProjectDir) || strings.Contains(arg, ArgTokenScanTarget) ||
			strings.Contains(arg, ArgTokenReportPath) {
			continue
		}
		if !looksLikePathArg(arg) {
			continue
		}
		if err := assertPathOutsideRoots(arg, workspaceRoots); err != nil {
			return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("argv[%d]=%q under workspace root", i, arg))
		}
	}
	return nil
}

// AssertBinaryOutsideRoots validates command[0] is a bare PATH name or an absolute
// path outside every workspace root (after EvalSymlinks).
func AssertBinaryOutsideRoots(command0 string, workspaceRoots []string) error {
	command0 = strings.TrimSpace(command0)
	if command0 == "" {
		return storeErr(RejectInvalidEntry, "empty command[0]")
	}
	if strings.Contains(command0, ArgTokenProjectDir) {
		return storeErr(RejectArgvProjectPathForbidden, "{{project_dir}} forbidden in command[0]")
	}
	if strings.Contains(command0, "{{") {
		return storeErr(RejectInvalidEntry, "unsupported template token in command[0]")
	}
	if !filepath.IsAbs(command0) {
		if strings.ContainsRune(command0, '/') || strings.ContainsRune(command0, '\\') {
			return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("relative command[0] %q forbidden", command0))
		}
		// Bare name — PATH lookup at run time.
		return nil
	}
	return assertPathOutsideRoots(command0, workspaceRoots)
}

func assertPathOutsideRoots(path string, workspaceRoots []string) error {
	var resolved string
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		resolved = eval
	} else if !os.IsNotExist(err) {
		// Missing target still checked via Abs of the given path.
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("resolve %q: %v", path, err))
		}
		resolved = abs
	} else {
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("abs %q: %v", path, absErr))
		}
		resolved = abs
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("abs %q: %v", path, err))
	}
	for _, root := range workspaceRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if isInsideRoot(abs, root) {
			return storeErr(RejectArgvProjectPathForbidden, fmt.Sprintf("path %q under workspace root %q", abs, root))
		}
	}
	return nil
}

func isInsideRoot(path, root string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if eval, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = eval
	}
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func looksLikePathArg(arg string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return false
	}
	if filepath.IsAbs(arg) {
		return true
	}
	return strings.ContainsRune(arg, filepath.Separator)
}
