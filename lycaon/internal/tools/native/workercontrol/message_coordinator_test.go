package workercontrol_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

type fakeMessageRecords struct {
	selfErr   error
	appendErr error
	ops       []string
	specs     []workerrecords.MessageSpec
	actors    []workerrecords.Actor
}

func (f *fakeMessageRecords) Self(_ context.Context, actor workerrecords.Actor) (workerrecords.Worker, error) {
	if f.selfErr != nil {
		return workerrecords.Worker{}, f.selfErr
	}
	return workerrecords.Worker{ID: "w-1", Name: "parser"}, nil
}

func (f *fakeMessageRecords) AppendMessage(_ context.Context, actor workerrecords.Actor, op workerrecords.Op, workerID string, spec workerrecords.MessageSpec) (workerrecords.MessageReceipt, error) {
	if f.appendErr != nil {
		return workerrecords.MessageReceipt{}, f.appendErr
	}
	f.ops, f.specs, f.actors = append(f.ops, op.ID), append(f.specs, spec), append(f.actors, actor)
	return workerrecords.MessageReceipt{WorkerID: workerID, Seq: int64(len(f.specs))}, nil
}

type wake struct{ parent, job string }

func runMessage(t *testing.T, records *fakeMessageRecords, args map[string]any, id tools.InvocationIdentity) ([]wake, string, error) {
	t.Helper()
	var wakes []wake
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterMessageCoordinatorTool(reg, workertools.MessageCoordinatorDeps{
		Records:    records,
		NeedsReply: func(_ context.Context, parent, job string) { wakes = append(wakes, wake{parent, job}) },
	}))
	out, err := reg.Run(context.Background(), workertools.MessageCoordinatorTool, args, tools.ToolContext{Identity: id})
	return wakes, out, err
}

var workerIdentity = tools.InvocationIdentity{SessionID: "child-1", ParentSessionID: "parent-1", WorkerJobID: "job-1", ToolCallID: "call-1"}

func TestMessageCoordinatorWakesOnlyForNeedsReply(t *testing.T) {
	records := &fakeMessageRecords{}
	wakes, _, err := runMessage(t, records, map[string]any{"text": "the encoding is the real defect", "needs_reply": true}, workerIdentity)
	testutil.FailErr(t, "needs reply", err)
	if len(wakes) != 1 || wakes[0] != (wake{"parent-1", "job-1"}) {
		t.Fatalf("wakes = %v", wakes)
	}
	if got := records.actors[0]; got.Role != workerrecords.RoleWorker || got.SessionID != "child-1" {
		t.Fatalf("sender = %+v", got)
	}
	if !records.specs[0].NeedsReply || records.specs[0].Body != "the encoding is the real defect" {
		t.Fatalf("stored = %+v", records.specs[0])
	}
	wakes, _, err = runMessage(t, records, map[string]any{"text": "also touched the caller"}, tools.InvocationIdentity{SessionID: "child-1", ParentSessionID: "parent-1", WorkerJobID: "job-1", ToolCallID: "call-2"})
	testutil.FailErr(t, "plain", err)
	if len(wakes) != 0 {
		t.Fatalf("plain message woke the coordinator: %v", wakes)
	}
}

// The operation identity is the tool call, so a replayed call stores once.
func TestMessageCoordinatorOperationIsTheToolCall(t *testing.T) {
	records := &fakeMessageRecords{}
	_, _, err := runMessage(t, records, map[string]any{"text": "a", "reply_to": float64(3)}, workerIdentity)
	testutil.FailErr(t, "send", err)
	if records.ops[0] != "message_coordinator:child-1:call-1" || records.specs[0].ReplyToSeq != 3 {
		t.Fatalf("op = %q spec = %+v", records.ops[0], records.specs[0])
	}
}

func TestMessageCoordinatorRejections(t *testing.T) {
	cases := []struct {
		name    string
		records *fakeMessageRecords
		id      tools.InvocationIdentity
		want    string
	}{
		{"addressed session", &fakeMessageRecords{}, tools.InvocationIdentity{SessionID: "s1", ToolCallID: "c"}, "MESSAGE_COORDINATOR_ADDRESSED_SESSION"},
		{"job-only leg", &fakeMessageRecords{selfErr: workerrecords.ErrWorkerNotFound}, workerIdentity, "MESSAGE_COORDINATOR_NO_WORKER"},
		{"unknown reply", &fakeMessageRecords{appendErr: workerrecords.ErrReplyUnknown}, workerIdentity, "MESSAGE_COORDINATOR_REPLY_UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wakes, _, err := runMessage(t, tc.records, map[string]any{"text": "x", "needs_reply": true}, tc.id)
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != tc.want {
				t.Fatalf("err = %v want %s", err, tc.want)
			}
			if len(wakes) != 0 {
				t.Fatalf("rejected message woke the coordinator: %v", wakes)
			}
		})
	}
	for _, bad := range []any{float64(0), float64(1.5), "2"} {
		if _, _, err := runMessage(t, &fakeMessageRecords{}, map[string]any{"text": "x", "reply_to": bad}, workerIdentity); err == nil {
			t.Fatalf("reply_to %v accepted", bad)
		}
	}
}
