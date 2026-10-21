package commandprogram

import (
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestOperationRequestBindsPlanNodeToChildInvocation(t *testing.T) {
	invocation := fixtureInvocation()
	invocation.Span = toolcontract.SourceSpan{StartByte: 4, EndByte: 12, Line: 1, Column: 5}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(invocation.Identity.NodeID), Span: invocation.Span}}
	request := OperationRequest{Invocation: invocation, OperationID: "read", Statement: statement, Argv: argvFields("cat", "file")}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OperationRequest){
		"operation": func(r *OperationRequest) { r.OperationID = "" },
		"argv":      func(r *OperationRequest) { r.Argv = nil },
		"span":      func(r *OperationRequest) { r.Statement.Span.EndByte++ },
		"node":      func(r *OperationRequest) { r.Statement.ID = "other" },
	} {
		changed := request
		mutate(&changed)
		if changed.Validate() == nil {
			t.Errorf("%s mismatch accepted", name)
		}
	}
	if got := request.Argv.Values(); len(got) != 2 || got[1] != "file" {
		t.Fatalf("argv values %q", got)
	}
}

func TestBytePipeConsumerEndClosesAsConsumerStop(t *testing.T) {
	budget, err := NewByteBudget("stream_bytes", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := NewBytePipe(t.Context(), budget)
	if err != nil {
		t.Fatal(err)
	}
	streams := Streams{Input: pipe.ConsumerEnd()}
	written := make(chan error, 1)
	go func() {
		_, err := pipe.Write([]byte("first"))
		if err == nil {
			_, err = pipe.Write([]byte("blocked"))
		}
		written <- err
	}()
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(streams.Input, buffer); err != nil || string(buffer) != "first" {
		t.Fatalf("read %q %v", buffer, err)
	}
	if err := streams.Input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-written; !errors.Is(err, ErrConsumerClosed) {
		t.Fatalf("producer saw %v, want consumer closure", err)
	}
	if got := pipe.Completion().Termination; got != StreamConsumerClosed {
		t.Fatalf("termination %s", got)
	}
}
