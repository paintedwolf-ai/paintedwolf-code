package hitl

import (
	"context"
	"testing"
	"time"
)

// I/O past an approval wait keeps the caller's cancellation but not its
// deadline, which the wait may have spent.
func TestIOContextFollowsCallerCancellationNotItsDeadline(t *testing.T) {
	stop, endSession := context.WithCancel(context.Background())
	defer endSession()

	caller, cancel := context.WithCancel(WithStopContext(context.Background(), stop))
	io, release := IOContext(caller, time.Hour)
	defer release()
	cancel()
	<-io.Done()

	expired, expire := context.WithDeadline(WithStopContext(context.Background(), stop), time.Unix(0, 0))
	defer expire()
	<-expired.Done()
	io, release = IOContext(expired, time.Hour)
	defer release()
	if err := io.Err(); err != nil {
		t.Fatalf("an expired caller deadline ended I/O: %v", err)
	}
	endSession()
	<-io.Done()

	plain, plainCancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer plainCancel()
	io, release = IOContext(plain, time.Hour)
	defer release()
	if io.Err() == nil {
		t.Fatal("without a stop context the caller's deadline must bound I/O")
	}
}
