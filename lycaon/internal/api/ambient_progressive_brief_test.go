package api

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/repoinfo"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type warmTrackingRepo struct {
	warmed     []string
	briefCalls int
}

func (p *warmTrackingRepo) Brief(context.Context, string) (*repoinfo.Brief, error) {
	p.briefCalls++
	return &repoinfo.Brief{}, nil
}

func (*warmTrackingRepo) KnownEmpty(context.Context, string) (bool, error) { return false, nil }
func (p *warmTrackingRepo) Warm(path string)                               { p.warmed = append(p.warmed, path) }

func (*warmTrackingRepo) Changed(context.Context, string) {}

func (*warmTrackingRepo) SetOnSettled(func(string)) {}

func (*warmTrackingRepo) Close() error { return nil }

func TestWarmRepoBriefDelegatesWithoutReading(t *testing.T) {
	p := &warmTrackingRepo{}
	s := NewServer(requiredTestDeps(t, Dependencies{Board: &board.SnapshotBuilder{Repo: p}}), nil, TestAPIToken)

	s.Git.WarmRepoBrief(" /project ")
	if len(p.warmed) != 1 || p.warmed[0] != "/project" {
		t.Fatalf("warm paths = %v", p.warmed)
	}
	_ = s.SessionAdmin.AttachAmbientOnSessionCreate(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, "sess-test")
	if p.briefCalls != 0 {
		t.Fatalf("brief calls = %d", p.briefCalls)
	}
}
