package attention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubCandidates struct {
	candidates []Candidate
	err        error
}

func (s stubCandidates) ListAttentionCandidates(context.Context) ([]Candidate, error) {
	return s.candidates, s.err
}

type stubCheckpoints struct {
	oldest map[string]time.Time
	reads  int
	err    error
}

func (s *stubCheckpoints) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.oldest, nil
}

// stubAsks holds each open ask by the time it was asked.
type stubAsks struct {
	open  map[string]time.Time
	asked []string
	err   error
}

func (s *stubAsks) PendingAsk(_ context.Context, sessionID string) (time.Time, bool, error) {
	s.asked = append(s.asked, sessionID)
	if s.err != nil {
		return time.Time{}, false, s.err
	}
	since, ok := s.open[sessionID]
	return since, ok, nil
}

type stubFinishes struct {
	latest map[string]time.Time
	reads  int
	err    error
}

func (s *stubFinishes) LatestFinishes(context.Context) (map[string]time.Time, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.latest, nil
}

type stubNamer struct{ names map[string]string }

func (s stubNamer) ProjectName(_ context.Context, projectID string) string {
	return s.names[projectID]
}

func at(min int) time.Time {
	return time.Date(2026, 7, 27, 12, min, 0, 0, time.UTC)
}

func candidate(id, projectID string, status api.SessionStatus, since time.Time) Candidate {
	return Candidate{SessionID: id, ProjectID: projectID, Status: status, StatusSince: since}
}

// read stamps a candidate as last seen at a time, the way the seen endpoint
// does when the person has the chat on screen.
func read(c Candidate, at time.Time) Candidate {
	c.SeenAt = &at
	return c
}

func build(t *testing.T, src *Source) api.AttentionView {
	t.Helper()
	view, err := src.BuildView(context.Background())
	testutil.FailErr(t, "BuildView", err)
	return view
}

// The store hands back idle chats only when something parks them on a person;
// an idle candidate with nothing pending and no open ask still yields no row.
func TestBuildViewOmitsIdleCandidatesWithNothingPending(t *testing.T) {
	src := &Source{Sessions: stubCandidates{candidates: []Candidate{
		candidate("s1", "p1", api.SessionStatusIdle, at(0)),
		candidate("s2", "p1", api.SessionStatusBusy, at(0)),
	}}}
	view := build(t, src)
	if len(view.Rows) != 1 {
		t.Fatalf("want 1 non-idle row, got %d: %+v", len(view.Rows), view.Rows)
	}
	if view.Rows[0].SessionID != "s2" {
		t.Fatalf("want the busy session, got %q", view.Rows[0].SessionID)
	}
}

// Work that finished while the person was elsewhere is a result waiting, even
// though nothing is blocked.
func TestIdleCandidateWithUnreadFinishIsFinished(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusIdle, at(9))}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"s1": at(5)}},
	}
	view := build(t, src)
	if len(view.Rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(view.Rows), view.Rows)
	}
	row := view.Rows[0]
	if row.Class != api.AttentionClassFinished {
		t.Fatalf("want finished, got %q", row.Class)
	}
	if row.Reason != api.AttentionReasonTurnFinished {
		t.Fatalf("want turn_finished reason, got %q", row.Reason)
	}
	// The completion time, not the session's last write: the row is about when
	// the result appeared.
	if !row.SinceAt.Equal(at(5)) {
		t.Fatalf("since must be the completion time, got %s", row.SinceAt)
	}
}

// A finish the person has already seen is not attention, however recent.
func TestFinishAlreadyReadYieldsNoRow(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			read(candidate("s1", "p1", api.SessionStatusIdle, at(9)), at(7)),
		}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"s1": at(5)}},
	}
	if view := build(t, src); len(view.Rows) != 0 {
		t.Fatalf("a finish older than the seen mark is read, got %+v", view.Rows)
	}
}

// Seeing a chat once does not make every later turn read.
func TestFinishAfterTheSeenMarkIsUnreadAgain(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			read(candidate("s1", "p1", api.SessionStatusIdle, at(9)), at(5)),
		}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"s1": at(7)}},
	}
	view := build(t, src)
	if len(view.Rows) != 1 || view.Rows[0].Class != api.AttentionClassFinished {
		t.Fatalf("want a finished row, got %+v", view.Rows)
	}
}

// A finish exactly at the seen mark is read: the stamp is written while the
// chat is on screen, so a same-instant completion was visible.
func TestFinishAtTheSeenMarkIsRead(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			read(candidate("s1", "p1", api.SessionStatusIdle, at(5)), at(5)),
		}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"s1": at(5)}},
	}
	if view := build(t, src); len(view.Rows) != 0 {
		t.Fatalf("want no row for a finish at the seen mark, got %+v", view.Rows)
	}
}

// A session that never completed a turn has nothing to read, seen mark or not.
func TestIdleCandidateWithNoFinishYieldsNoRow(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusIdle, at(9))}},
		Finishes: &stubFinishes{latest: map[string]time.Time{}},
	}
	if view := build(t, src); len(view.Rows) != 0 {
		t.Fatalf("want no row without a completed turn, got %+v", view.Rows)
	}
}

// A new turn is the headline, not the previous result.
func TestBusySessionWithUnreadFinishStaysRunning(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusBusy, at(9))}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"s1": at(5)}},
	}
	view := build(t, src)
	if len(view.Rows) != 1 || view.Rows[0].Class != api.AttentionClassRunning {
		t.Fatalf("want running, got %+v", view.Rows)
	}
}

// Being asked something outranks having something to read.
func TestPendingCheckpointOutranksUnreadFinish(t *testing.T) {
	src := &Source{
		Sessions:    stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusIdle, at(9))}},
		Checkpoints: &stubCheckpoints{oldest: map[string]time.Time{"s1": at(2)}},
		Finishes:    &stubFinishes{latest: map[string]time.Time{"s1": at(5)}},
	}
	view := build(t, src)
	if len(view.Rows) != 1 || view.Rows[0].Class != api.AttentionClassNeedsYou {
		t.Fatalf("want needs_you, got %+v", view.Rows)
	}
}

// Worst first: an obligation, then a failure, then a result to read, then work
// still in flight.
func TestFinishedSortsBehindErrorAndAheadOfRunning(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			candidate("running", "p1", api.SessionStatusBusy, at(0)),
			candidate("finished", "p1", api.SessionStatusIdle, at(0)),
			candidate("failed", "p1", api.SessionStatusError, at(0)),
		}},
		Finishes: &stubFinishes{latest: map[string]time.Time{"finished": at(1)}},
	}
	view := build(t, src)
	got := make([]string, 0, len(view.Rows))
	for _, row := range view.Rows {
		got = append(got, row.SessionID)
	}
	if len(got) != 3 || got[0] != "failed" || got[1] != "finished" || got[2] != "running" {
		t.Fatalf("want failed, finished, running; got %v", got)
	}
}

// A failed read is not evidence that nothing finished. The snapshot aborts so
// clients keep their last known view.
func TestFinishReadFailureAbortsTheSnapshot(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusIdle, at(0))}},
		Finishes: &stubFinishes{err: errors.New("turns unavailable")},
	}
	if _, err := src.BuildView(context.Background()); err == nil {
		t.Fatal("want the build to fail when the finish read fails")
	}
}

// An unconfigured finish source contributes no rows rather than panicking.
func TestNilFinishSourceYieldsNoFinishedRows(t *testing.T) {
	src := &Source{Sessions: stubCandidates{candidates: []Candidate{
		candidate("s1", "p1", api.SessionStatusIdle, at(0)),
	}}}
	if view := build(t, src); len(view.Rows) != 0 {
		t.Fatalf("want no rows without a finish source, got %+v", view.Rows)
	}
}

// A busy session holding a pending checkpoint is waiting on a person, not
// running.
func TestPendingCheckpointOutranksBusyStatus(t *testing.T) {
	src := &Source{
		Sessions:    stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusBusy, at(5))}},
		Checkpoints: &stubCheckpoints{oldest: map[string]time.Time{"s1": at(3)}},
	}
	view := build(t, src)
	row := view.Rows[0]
	if row.Class != api.AttentionClassNeedsYou {
		t.Fatalf("want needs_you, got %q", row.Class)
	}
	if row.Reason != api.AttentionReasonCheckpoint {
		t.Fatalf("want checkpoint reason, got %q", row.Reason)
	}
	if !row.SinceAt.Equal(at(3)) {
		t.Fatalf("since must be the checkpoint issue time, got %s", row.SinceAt)
	}
}

func TestIdleCandidateWithPendingCheckpointNeedsYou(t *testing.T) {
	src := &Source{
		Sessions:    stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusIdle, at(9))}},
		Checkpoints: &stubCheckpoints{oldest: map[string]time.Time{"s1": at(2)}},
	}
	view := build(t, src)
	if len(view.Rows) != 1 || !view.Rows[0].SinceAt.Equal(at(2)) {
		t.Fatalf("want one needs_you row since the oldest pending checkpoint, got %+v", view.Rows)
	}
}

// Pending checkpoints are read once per build, not once per candidate, and a
// candidate already explained by a checkpoint never consults the ask latch.
func TestPendingCheckpointsReadOncePerBuild(t *testing.T) {
	checkpoints := &stubCheckpoints{oldest: map[string]time.Time{"s1": at(1)}}
	asks := &stubAsks{}
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			candidate("s1", "p1", api.SessionStatusBusy, at(1)),
			candidate("s2", "p1", api.SessionStatusBusy, at(2)),
			candidate("s3", "p2", api.SessionStatusBusy, at(3)),
		}},
		Checkpoints: checkpoints,
		Asks:        asks,
	}
	view := build(t, src)
	if len(view.Rows) != 3 {
		t.Fatalf("want three rows, got %+v", view.Rows)
	}
	if checkpoints.reads != 1 {
		t.Fatalf("pending checkpoints read %d times, want 1", checkpoints.reads)
	}
	if len(asks.asked) != 2 || asks.asked[0] != "s2" || asks.asked[1] != "s3" {
		t.Fatalf("ask latch consulted for %v, want only the sessions without a checkpoint", asks.asked)
	}
}

// An open ask is aged from the moment it was asked, not from the session's
// status change: the turn went busy long before it parked on the person.
func TestOpenAskLatchIsNeedsYouSinceTheAsk(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusBusy, at(4))}},
		Asks:     &stubAsks{open: map[string]time.Time{"s1": at(7)}},
	}
	view := build(t, src)
	if view.Rows[0].Class != api.AttentionClassNeedsYou || view.Rows[0].Reason != api.AttentionReasonAsk {
		t.Fatalf("open ask latch must read as needs_you/ask: %+v", view.Rows[0])
	}
	if !view.Rows[0].SinceAt.Equal(at(7)) {
		t.Fatalf("since must be the ask time, got %s", view.Rows[0].SinceAt)
	}
}

// Asks that wait longer sort first, whatever their sessions last did.
func TestOpenAsksSortByAskAge(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			candidate("recent-ask", "p1", api.SessionStatusBusy, at(1)),
			candidate("old-ask", "p1", api.SessionStatusBusy, at(9)),
		}},
		Asks: &stubAsks{open: map[string]time.Time{"recent-ask": at(8), "old-ask": at(3)}},
	}
	view := build(t, src)
	if len(view.Rows) != 2 || view.Rows[0].SessionID != "old-ask" {
		t.Fatalf("want the older ask first, got %+v", view.Rows)
	}
}

func TestErrorStatusIsItsOwnClass(t *testing.T) {
	src := &Source{Sessions: stubCandidates{candidates: []Candidate{
		candidate("s1", "p1", api.SessionStatusError, at(1)),
	}}}
	view := build(t, src)
	if view.Rows[0].Class != api.AttentionClassError || view.Rows[0].Reason != api.AttentionReasonTurnError {
		t.Fatalf("want error/turn_error: %+v", view.Rows[0])
	}
}

// Worst class first, then longest-waiting first inside a class.
func TestRowsSortByClassThenAge(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			candidate("running-old", "p1", api.SessionStatusBusy, at(1)),
			candidate("errored", "p1", api.SessionStatusError, at(6)),
			candidate("blocked-new", "p1", api.SessionStatusIdle, at(7)),
			candidate("blocked-old", "p2", api.SessionStatusIdle, at(7)),
		}},
		Checkpoints: &stubCheckpoints{oldest: map[string]time.Time{
			"blocked-new": at(5),
			"blocked-old": at(2),
		}},
	}
	view := build(t, src)
	got := make([]string, 0, len(view.Rows))
	for _, row := range view.Rows {
		got = append(got, row.SessionID)
	}
	want := []string{"blocked-old", "blocked-new", "errored", "running-old"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("want order %v, got %v", want, got)
		}
	}
}

func TestProjectNameResolvedOncePerProject(t *testing.T) {
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{
			candidate("s1", "p1", api.SessionStatusBusy, at(1)),
			candidate("s2", "p1", api.SessionStatusBusy, at(2)),
		}},
		Projects: stubNamer{names: map[string]string{"p1": "Painted Wolf"}},
	}
	view := build(t, src)
	for _, row := range view.Rows {
		if row.ProjectName != "Painted Wolf" {
			t.Fatalf("want project name on every row, got %q", row.ProjectName)
		}
	}
}

func TestCheckpointReadErrorFailsTheBuild(t *testing.T) {
	failure := errors.New("store down")
	src := &Source{
		Sessions:    stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusBusy, at(1))}},
		Checkpoints: &stubCheckpoints{err: failure},
	}
	if _, err := src.BuildView(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("checkpoint read failure must surface, got %v", err)
	}
}

func TestAskReadErrorFailsTheBuild(t *testing.T) {
	failure := errors.New("store down")
	src := &Source{
		Sessions: stubCandidates{candidates: []Candidate{candidate("s1", "p1", api.SessionStatusBusy, at(1))}},
		Asks:     &stubAsks{err: failure},
	}
	if _, err := src.BuildView(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("ask read failure must surface, got %v", err)
	}
}

func TestCandidateReadErrorFailsTheBuild(t *testing.T) {
	src := &Source{Sessions: stubCandidates{err: errors.New("store down")}}
	if _, err := src.BuildView(context.Background()); err == nil {
		t.Fatal("a failed candidate read must surface, not publish an empty view")
	}
}

func TestNilSourceReturnsEmptyView(t *testing.T) {
	var src *Source
	view := build(t, src)
	if len(view.Rows) != 0 {
		t.Fatalf("want empty view, got %+v", view.Rows)
	}
	if view.Rows == nil {
		t.Fatal("rows must serialize as [] rather than null")
	}
}
