package recall

import (
	"fmt"
	"strings"
)

// Widen is the scope escape hatch. It is a closed argument rather than a query
// token so a caller cannot widen itself by rewording its query; session: and
// project: only narrow within the granted reach.
type Widen string

const (
	WidenDefault Widen = ""
	WidenSubtree Widen = "subtree"
	WidenProject Widen = "project"
	WidenAll     Widen = "all"
)

// ParseWiden validates a stated widening.
func ParseWiden(raw string) (Widen, error) {
	switch w := Widen(strings.ToLower(strings.TrimSpace(raw))); w {
	case WidenDefault, WidenSubtree, WidenProject, WidenAll:
		return w, nil
	default:
		return "", fmt.Errorf("unknown widen %q", raw)
	}
}

// Role is derived from session topology, not from the agent's claim about
// itself.
type Role string

const (
	RoleWorker Role = "worker"
	RoleRoot   Role = "root"
)

// Caller identifies the session asking, as the host knows it.
type Caller struct {
	SessionID       string
	ParentSessionID string
	ProjectID       string
}

// Role reports the caller's topology role.
func (c Caller) Role() Role {
	if strings.TrimSpace(c.ParentSessionID) != "" {
		return RoleWorker
	}
	return RoleRoot
}

// Scope is the resolved reach of one call.
type Scope struct {
	Role Role
	// Sessions bounds rows to exact sessions. Empty means no predicate.
	Sessions []string
	// RootSession bounds rows to one session tree by its stamped root.
	RootSession string
	// ProjectID narrows to one project. Empty means every attached project.
	ProjectID string
	Widen     Widen
	// Stated is echoed on the result so a caller never assumes its query's
	// scope was honored.
	Stated string
}

// ErrScopeDenied is returned when a caller asks for reach it does not have.
type ErrScopeDenied struct {
	Role  Role
	Widen Widen
}

func (e *ErrScopeDenied) Error() string {
	return fmt.Sprintf("recall: %s may not widen to %q", e.Role, e.Widen)
}

// resolveScope derives reach from the caller's topology.
//
// A worker sees its own session only — a peer's observations are context it was
// never delegated. A root session scopes by the root stamped on each row, so
// archived and retention-deleted legs stay reachable.
func resolveScope(caller Caller, widen Widen) (Scope, error) {
	sessionID := strings.TrimSpace(caller.SessionID)
	if sessionID == "" {
		return Scope{}, fmt.Errorf("recall: caller session required")
	}
	role := caller.Role()
	project := strings.TrimSpace(caller.ProjectID)

	if role == RoleWorker {
		if widen != WidenDefault {
			return Scope{}, &ErrScopeDenied{Role: role, Widen: widen}
		}
		return Scope{
			Role:      role,
			Sessions:  []string{sessionID},
			ProjectID: project,
			Widen:     WidenDefault,
			Stated:    "this worker leg only",
		}, nil
	}

	switch widen {
	case WidenProject:
		return Scope{
			Role:      role,
			ProjectID: project,
			Widen:     WidenProject,
			Stated:    "every session in this project",
		}, nil
	case WidenAll:
		return Scope{Role: role, Widen: WidenAll, Stated: "every session in every attached project"}, nil
	case WidenDefault, WidenSubtree:
	}

	return Scope{
		Role:        role,
		RootSession: sessionID,
		ProjectID:   project,
		Widen:       WidenSubtree,
		Stated:      "this session and its worker legs, finished ones included",
	}, nil
}
