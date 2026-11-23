package clisocket

import (
	"context"
	"errors"
	"testing"
)

type modelFixture struct {
	model, path string
	optedOut    bool
	err         error
}

func (m *modelFixture) ImportLocalModel(_ context.Context, model, path string) error {
	m.model, m.path = model, path
	return m.err
}
func (m *modelFixture) OptOutLocalModel(context.Context) error { m.optedOut = true; return m.err }
func TestLocalModelRequests(t *testing.T) {
	m := &modelFixture{}
	s := &Server{models: m}
	good := Request{Op: OpImportModel, Model: "e4b", Target: t.TempDir()}
	if r := s.Handle(t.Context(), good); r.Status != StatusOK || m.path != good.Target || m.model != good.Model {
		t.Fatalf("import %+v, service %+v", r, m)
	}
	if r := s.Handle(t.Context(), Request{Op: OpOptOutModel}); r.Status != StatusOK || !m.optedOut {
		t.Fatalf("opt out %+v", r)
	}
	for _, invalid := range []Request{{Op: OpImportModel, Model: "e4b", Target: "relative"}, {Op: OpImportModel, Target: t.TempDir()}, {Op: OpOptOutModel, Target: "x"}} {
		if r := s.Handle(t.Context(), invalid); r.Status != StatusError {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	m.err = errors.New("model unavailable")
	if r := s.Handle(t.Context(), good); r.Status != StatusError || r.Message != m.err.Error() {
		t.Fatalf("service failure %+v", r)
	}
	s.models = nil
	if r := s.Handle(t.Context(), good); r.Status != StatusError {
		t.Fatal("missing service accepted request")
	}
}
