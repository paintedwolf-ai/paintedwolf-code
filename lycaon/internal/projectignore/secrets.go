package projectignore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

var ErrUnavailable = errors.New("secret ignores are unavailable")
var ErrUntrusted = errors.New("this project's scanning and ignores are not trusted, so its ignores do not apply")

// SecretEntry preserves the exact bytes of a public value.
type SecretEntry struct {
	ID      string `yaml:"id,omitempty" json:"id,omitempty"`
	Value   string `yaml:"value" json:"value"`
	Reason  string `yaml:"reason" json:"reason"`
	Expires string `yaml:"expires,omitempty" json:"expires,omitempty"`
}

func (e SecretEntry) Validate() error {
	if e.Value == "" || !utf8.ValidString(e.Value) || len(e.Value) > 65536 || strings.ContainsRune(e.Value, 0) {
		return fmt.Errorf("%w: value must be nonempty UTF-8 text of at most 64 KiB without NUL", ErrInvalid)
	}
	if strings.TrimSpace(e.Reason) == "" {
		return fmt.Errorf("%w: a reason is required", ErrInvalid)
	}
	if e.Expires != "" {
		if _, err := time.Parse("2006-01-02", e.Expires); err != nil {
			return fmt.Errorf("%w: expires must be YYYY-MM-DD", ErrInvalid)
		}
	}
	return nil
}

func (e SecretEntry) Expired(now time.Time) bool {
	if e.Expires == "" {
		return false
	}
	at, err := time.Parse("2006-01-02", e.Expires)
	return err != nil || !now.Before(at)
}

func (e SecretEntry) Key() string {
	if e.ID != "" {
		return e.ID
	}
	b, err := json.Marshal(e)
	if err != nil {
		// SecretEntry is a plain string struct; Marshal cannot fail here.
		return "entry:" + Digest(nil)
	}
	return "entry:" + Digest(b)
}

type Root struct{ ID, Path string }
type Defect struct {
	Index      int
	ID, Reason string
	RootID     string
	Path       string
}
type SecretRule struct {
	SecretEntry
	RootID, Path, Status string
}
type SecretCatalog struct {
	Rules   []SecretRule
	Invalid []Defect
}

func ReadSecrets(root string) ([]SecretEntry, []Defect, error) {
	data, err := Read(root)
	if err != nil {
		return nil, nil, err
	}
	doc, err := Parse(data)
	if err != nil {
		return nil, nil, err
	}
	list, err := Section(doc, "secrets")
	if err != nil {
		return nil, nil, err
	}
	var entries []SecretEntry
	var defects []Defect
	seen := map[string]bool{}
	for i, node := range list.Content {
		var e SecretEntry
		raw, err := yaml.Marshal(node)
		if err == nil {
			err = config.DecodeYAML(raw, &e)
		}
		if err == nil {
			err = e.Validate()
		}
		if err == nil && e.ID != "" && seen[e.ID] {
			err = fmt.Errorf("duplicate entry id")
		}
		if e.ID != "" {
			seen[e.ID] = true
		}
		if err != nil {
			defects = append(defects, Defect{Index: i, ID: e.ID, Reason: err.Error()})
			continue
		}
		entries = append(entries, e)
	}
	// Duplicate identifiers invalidate every conflicting declaration.
	for _, d := range defects {
		if d.ID != "" {
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].ID == d.ID {
					entries = append(entries[:i], entries[i+1:]...)
				}
			}
		}
	}
	return entries, defects, nil
}

type SecretService struct {
	Trusted   func(context.Context, string) bool
	Changed   func(context.Context, string)
	Reviews   SecretReviews
	Roots     func(context.Context, string) ([]Root, error)
	Protected func(context.Context, string, string) bool
}

func (s *SecretService) List(ctx context.Context, projectID string) (SecretCatalog, error) {
	out := SecretCatalog{Rules: []SecretRule{}, Invalid: []Defect{}}
	if s == nil || s.Roots == nil {
		return out, ErrUnavailable
	}
	roots, err := s.Roots(ctx, projectID)
	if err != nil {
		return out, err
	}
	for _, root := range roots {
		entries, defects, err := ReadSecrets(root.Path)
		if err != nil {
			defects = append(defects, Defect{Index: -1, Reason: err.Error()})
		}
		for _, defect := range defects {
			defect.RootID = root.ID
			defect.Path = Path(root.Path)
			defect.Reason = Path(root.Path) + ": " + defect.Reason
			out.Invalid = append(out.Invalid, defect)
		}
		for _, e := range entries {
			status := "active"
			if s.Trusted != nil && !s.Trusted(ctx, projectID) {
				status = "untrusted"
			}
			if e.Expired(time.Now().UTC()) {
				status = "expired"
			}
			if s.Protected != nil && s.Protected(ctx, projectID, e.Value) {
				status = "protected"
			}
			out.Rules = append(out.Rules, SecretRule{SecretEntry: e, RootID: root.ID, Path: Path(root.Path), Status: status})
		}
	}
	return out, nil
}

// ActiveValues re-reads declarations and authority; cached detections remain raw.
func (s *SecretService) ActiveValues(ctx context.Context, projectID string) []SecretEntry {
	if projectID == "" {
		return nil
	}
	catalog, err := s.List(ctx, projectID)
	if err != nil {
		return nil
	}
	var out []SecretEntry
	for _, rule := range catalog.Rules {
		if rule.Status == "active" {
			out = append(out, rule.SecretEntry)
		}
	}
	return out
}
