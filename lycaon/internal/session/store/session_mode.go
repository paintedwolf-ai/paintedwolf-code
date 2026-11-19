package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

var (
	ErrInvalidSessionMode     = errors.New("session mode must be agent or ask")
	ErrSessionModeLocked      = errors.New("a started Agent chat cannot switch to Ask")
	ErrSessionModeUnavailable = errors.New("root chat mode could not be verified")
	ErrChildSessionMode       = errors.New("worker sessions inherit the root chat mode")
)

// RootAuthority is the mode and admission latch owned by a root session.
type RootAuthority struct {
	RootSessionID string
	Mode          api.SessionMode
	Started       bool
}

// WithModeIntent validates a proposed transition without persisting it.
func (a RootAuthority) WithModeIntent(intent *api.SessionMode) (api.SessionMode, error) {
	if a.RootSessionID == "" || !validSessionMode(a.Mode) {
		return "", ErrSessionModeUnavailable
	}
	if intent == nil {
		return a.Mode, nil
	}
	if !validSessionMode(*intent) {
		return "", ErrInvalidSessionMode
	}
	if a.Started && a.Mode == api.SessionModeAgent && *intent == api.SessionModeAsk {
		return "", ErrSessionModeLocked
	}
	return *intent, nil
}

// modeRecords uses the caller's transaction or memory lock for reads and writes.
type modeRecords interface {
	ReadAuthority(context.Context, string) (RootAuthority, error)
	StoreAuthority(context.Context, RootAuthority) error
	SessionSnapshot(context.Context, string) (*api.Session, error)
}

func setModeSnapshot(ctx context.Context, records modeRecords, id string, mode api.SessionMode) (*api.Session, error) {
	if err := applyModeIntent(ctx, records, id, &mode, false, nil); err != nil {
		return nil, err
	}
	return records.SessionSnapshot(ctx, id)
}

func applyModeIntent(ctx context.Context, records modeRecords, id string, intent *api.SessionMode, start bool, validate func(api.SessionMode) error) error {
	root, err := records.ReadAuthority(ctx, id)
	if err != nil {
		return err
	}
	if intent != nil && root.RootSessionID != id {
		return ErrChildSessionMode
	}
	mode, err := root.WithModeIntent(intent)
	if err != nil {
		return err
	}
	if validate != nil {
		if err := validate(mode); err != nil {
			return err
		}
	}
	started := start || root.Started
	if root.Mode == mode && root.Started == started {
		return nil
	}
	root.Mode, root.Started = mode, started
	return records.StoreAuthority(ctx, root)
}

func validSessionMode(mode api.SessionMode) bool {
	return mode == api.SessionModeAgent || mode == api.SessionModeAsk
}

func initialSessionMode(intent *api.SessionMode) (api.SessionMode, error) {
	if intent == nil {
		return api.SessionModeAgent, nil
	}
	if !validSessionMode(*intent) {
		return "", ErrInvalidSessionMode
	}
	return *intent, nil
}

func sessionModeUnavailable(id string, cause error) error {
	return fmt.Errorf("%w for session %q: %w", ErrSessionModeUnavailable, id, cause)
}
