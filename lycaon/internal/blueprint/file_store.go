package blueprint

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/pkg/api"
)

// RootResolver maps a project id to its primary workspace path.
type RootResolver func(ctx context.Context, projectID string) (string, error)

// FileStore implements Store against `<overlay>/blueprints/**/*.md` on disk.
type FileStore struct {
	ResolveRoot RootResolver
}

// NewFileStore creates a convention filesystem blueprint store.
func NewFileStore(resolve RootResolver) *FileStore {
	return &FileStore{ResolveRoot: resolve}
}

func (s *FileStore) root(ctx context.Context, projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project_id required")
	}
	if s == nil || s.ResolveRoot == nil {
		return "", fmt.Errorf("project root resolver not configured")
	}
	root, err := s.ResolveRoot(ctx, projectID)
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("project has no workspace root")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("project workspace root must be absolute")
	}
	return root, nil
}

// Create writes a new draft blueprint file under the convention.
func (s *FileStore) Create(ctx context.Context, bp *api.Blueprint) error {
	if bp == nil {
		return fmt.Errorf("blueprint required")
	}
	if bp.ID == "" {
		bp.ID = uuid.NewString()
	}
	root, err := s.root(ctx, bp.ProjectID)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(bp.Path)
	if path == "" {
		path = s.mintPath(root, bp.Title, "")
	}
	if err := ValidateConventionPath(path); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
		path = s.mintPath(root, bp.Title, "")
		if err := ValidateConventionPath(path); err != nil {
			return err
		}
	}
	content := bp.Content
	if strings.TrimSpace(content) == "" {
		content = EnsureTitleFrontmatter("", bp.Title)
	} else {
		content = ResetToDraftFrontmatter(content)
		if ParseTitleFrontmatter(content) == "" && strings.TrimSpace(bp.Title) != "" {
			content = EnsureTitleFrontmatter(content, bp.Title)
		}
	}
	content = EnsureIDFrontmatter(content, bp.ID)
	content = SetUpdatedAtFrontmatter(content, time.Now())
	// A destination created during planning belongs to another writer.
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: filepath.FromSlash(path)},
		Source:   strings.NewReader(content),
		Mode:     FileMode,
		DirMode:  DirMode,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			if _, statErr := target.Lstat(); statErr == nil {
				return ErrPathTaken
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			return nil
		},
	}); err != nil {
		return err
	}
	bp.Path = path
	bp.Content = content
	bp.Status = ParseStatusFrontmatter(content)
	if bp.Status == "" {
		bp.Status = api.BlueprintStatusDraft
	}
	if bp.Version == 0 {
		bp.Version = 1
	}
	bp.UpdatedAt, _ = ParseUpdatedAtFrontmatter(content)
	if title := ParseTitleFrontmatter(content); title != "" {
		bp.Title = title
	}
	return nil
}

// Get loads a blueprint by project-relative path.
func (s *FileStore) Get(ctx context.Context, projectID, path string) (*api.Blueprint, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if err := ValidateConventionPath(path); err != nil {
		return nil, ErrNotFound
	}
	root, err := s.root(ctx, projectID)
	if err != nil {
		return nil, err
	}
	abs := filepath.Join(root, filepath.FromSlash(path))
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	content := string(data)
	id := Identity(projectID, path, content)
	title := ParseTitleFrontmatter(content)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	updatedAt, _ := ParseUpdatedAtFrontmatter(content)
	return &api.Blueprint{
		ID:        id,
		ProjectID: projectID,
		Title:     title,
		Path:      path,
		Content:   content,
		Status:    ParseStatusFrontmatter(content),
		Version:   1,
		UpdatedAt: updatedAt,
	}, nil
}

// UpdateContent rejects changes based on stale content.
func (s *FileStore) UpdateContent(ctx context.Context, projectID, path, content, expectDigest string) (*api.Blueprint, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if err := ValidateConventionPath(path); err != nil {
		return nil, err
	}
	if strings.TrimSpace(expectDigest) == "" {
		return nil, fmt.Errorf("blueprint update requires the digest it was computed from")
	}
	root, err := s.root(ctx, projectID)
	if err != nil {
		return nil, err
	}
	existing, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	content = EnsureIDFrontmatter(content, Identity(projectID, path, string(existing)))
	content = SetUpdatedAtFrontmatter(content, time.Now())
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: filepath.FromSlash(path)},
		Source:   strings.NewReader(content),
		Mode:     FileMode,
		DirMode:  DirMode,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			return requireDigest(target, expectDigest)
		},
	}); err != nil {
		return nil, err
	}
	return s.Get(ctx, projectID, path)
}

// FreePath returns an unused convention path for title, treating except as free.
func (s *FileStore) FreePath(ctx context.Context, projectID, title, except string) (string, error) {
	root, err := s.root(ctx, projectID)
	if err != nil {
		return "", err
	}
	return s.mintPath(root, title, except), nil
}

func (s *FileStore) mintPath(root, title, except string) string {
	except = filepath.ToSlash(strings.TrimSpace(except))
	return MintUniquePath(title, time.Now(), func(rel string) bool {
		if except != "" && rel == except {
			return false
		}
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	})
}

// Rename moves a convention file to a free destination.
func (s *FileStore) Rename(ctx context.Context, projectID, from, to string) error {
	from = filepath.ToSlash(strings.TrimSpace(from))
	to = filepath.ToSlash(strings.TrimSpace(to))
	if err := ValidateConventionPath(from); err != nil {
		return err
	}
	if err := ValidateConventionPath(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	root, err := s.root(ctx, projectID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(from))); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(to))); err == nil {
		return ErrPathTaken
	} else if !os.IsNotExist(err) {
		return err
	}
	return fseffect.Rename(root, filepath.FromSlash(from), filepath.FromSlash(to))
}

// requireDigest refuses the commit unless the destination still holds want.
func requireDigest(target fseffect.Target, want string) error {
	f, err := target.Open()
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	defer func() { _ = f.Close() }()
	current, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if ContentDigest(string(current)) != want {
		return ErrContentChanged
	}
	return nil
}

// Delete removes the blueprint file at path.
func (s *FileStore) Delete(ctx context.Context, projectID, path string) error {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if err := ValidateConventionPath(path); err != nil {
		return ErrNotFound
	}
	root, err := s.root(ctx, projectID)
	if err != nil {
		return err
	}
	abs := filepath.Join(root, filepath.FromSlash(path))
	if err := os.Remove(abs); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// List walks the convention tree, newest first.
func (s *FileStore) List(ctx context.Context, projectID string) ([]api.BlueprintSummary, error) {
	root, err := s.root(ctx, projectID)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(root, filepath.FromSlash(BlueprintsDir()))
	var entries []api.BlueprintSummary
	_ = filepath.WalkDir(base, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return nil //nolint:nilerr // WalkDir visitor: skip a path outside the root, do not abort the scan
		}
		rel = filepath.ToSlash(rel)
		if err := ValidateConventionPath(rel); err != nil {
			return nil //nolint:nilerr // WalkDir visitor: skip a non-conventional path, do not abort the scan
		}
		data, err := os.ReadFile(abs) // #nosec G122 -- abs is Rel-checked under project root above
		if err != nil {
			return nil //nolint:nilerr // WalkDir visitor: skip the unreadable file, do not abort the scan
		}
		content := string(data)
		id := Identity(projectID, rel, content)
		title := ParseTitleFrontmatter(content)
		if title == "" {
			title = strings.TrimSuffix(name, filepath.Ext(name))
		}
		updatedAt, _ := ParseUpdatedAtFrontmatter(content)
		entries = append(entries, api.BlueprintSummary{
			ID:        id,
			Title:     title,
			Path:      rel,
			Status:    ParseStatusFrontmatter(content),
			Version:   1,
			UpdatedAt: updatedAt,
		})
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		}
		return entries[i].Path < entries[j].Path
	})
	if entries == nil {
		entries = []api.BlueprintSummary{}
	}
	return entries, nil
}

var _ Store = (*FileStore)(nil)
