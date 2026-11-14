// Package viewstate holds what each of a person's windows shows of a project
// right now. Reports live only while their window renews them.
package viewstate

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// TTL is how long a report holds without renewal.
const TTL = time.Minute

// MaxWindows bounds the windows one person's view reads, most recently focused first.
const MaxWindows = 8

// ErrClientIdentity means a window reported under another person.
var ErrClientIdentity = errors.New("view client belongs to another person")

// Report is one window's view of a project.
type Report struct {
	ClientID, PersonID string
	Focused            bool
	// FocusedAt is when the window last reported focus; zero if it never has.
	FocusedAt, UpdatedAt time.Time
	View                 api.ReplaceProjectViewRequest
}

// Empty reports that the window shows nothing worth telling.
func (r Report) Empty() bool {
	v := r.View
	return v.Editor == nil && v.Diff == nil && v.Folder == nil && v.Search == nil && len(v.Findings) == 0
}

type Service struct {
	mu      sync.Mutex
	now     func() time.Time
	reports map[string]map[string]Report
}

func New(now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{now: now, reports: map[string]map[string]Report{}}
}

// Replace records a window's report; a report that shows nothing withdraws the window.
func (s *Service) Replace(projectID, personID string, view api.ReplaceProjectViewRequest) error {
	projectID, personID = strings.TrimSpace(projectID), strings.TrimSpace(personID)
	client := strings.TrimSpace(view.ClientID)
	if projectID == "" || personID == "" || client == "" {
		return ErrClientIdentity
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	for _, windows := range s.reports {
		if previous, ok := windows[client]; ok && previous.PersonID != personID {
			return ErrClientIdentity
		}
	}
	windows := s.reports[projectID]
	previous := windows[client]
	report := Report{ClientID: client, PersonID: personID, Focused: view.Focused, FocusedAt: previous.FocusedAt, UpdatedAt: now, View: view}
	if view.Focused {
		report.FocusedAt = now
	}
	if report.Empty() {
		delete(windows, client)
		return nil
	}
	if windows == nil {
		windows = map[string]Report{}
		s.reports[projectID] = windows
	}
	windows[client] = report
	return nil
}

// Visible lists a person's live reports for a project, most recently focused first.
func (s *Service) Visible(projectID, personID string) []Report {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	var out []Report
	for _, report := range s.reports[strings.TrimSpace(projectID)] {
		if report.PersonID == strings.TrimSpace(personID) {
			out = append(out, report)
		}
	}
	slices.SortFunc(out, func(a, b Report) int {
		if c := b.FocusedAt.Compare(a.FocusedAt); c != 0 {
			return c
		}
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ClientID, b.ClientID)
	})
	return out[:min(len(out), MaxWindows)]
}

func (s *Service) pruneLocked(now time.Time) {
	for projectID, windows := range s.reports {
		for client, report := range windows {
			if now.Sub(report.UpdatedAt) >= TTL {
				delete(windows, client)
			}
		}
		if len(windows) == 0 {
			delete(s.reports, projectID)
		}
	}
}
