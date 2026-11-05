package nativegen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/inference"
)

func TestMain(m *testing.M) {
	if os.Getenv("PW_NATIVEGEN_PROCESS_FIXTURE") == "3" {
		inventoryFixture()
		os.Exit(0)
	}
	if os.Getenv("PW_NATIVEGEN_PROCESS_FIXTURE") == "2" {
		runtimeFixture()
		os.Exit(0)
	}
	if os.Getenv("PW_NATIVEGEN_PROCESS_FIXTURE") == "1" {
		processFixture()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func processFixture() {
	for {
		payload, err := readProtocolFrame(os.Stdin)
		if err != nil {
			return
		}
		var request struct {
			Version uint32  `json:"version"`
			ID      uint64  `json:"id"`
			Command command `json:"command"`
		}
		if json.Unmarshal(payload, &request) != nil {
			os.Exit(2)
		}
		reply := map[string]any{"status": "ok", "id": request.ID, "result": map[string]any{"kind": "cleared"}}
		switch request.Command.Method {
		case "hang", "advance":
			if request.Command.Method == "advance" && os.Getenv("PW_NATIVEGEN_STEP_HANG") != "1" {
				break
			}
			// Reading another frame keeps the process alive without replying.
			_, _ = readProtocolFrame(os.Stdin)
			return
		case "invalid":
			reply["result"] = map[string]any{"kind": "cleared", "unexpected": true}
		case "capacity":
			delete(reply, "result")
			reply["status"], reply["code"], reply["message"] = "error", "capacity", "fixture capacity"
		}
		payload, err = json.Marshal(reply)
		if err != nil || writeProtocolFrame(os.Stdout, payload) != nil {
			return
		}
	}
}

func fixtureProcess(t *testing.T) (*rpcProcess, *inference.Device, *inference.MemoryBudget) {
	t.Helper()
	t.Setenv("PW_NATIVEGEN_PROCESS_FIXTURE", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	device, _ := inference.NewDevice(4)
	budget, _ := inference.NewMemoryBudget(100)
	reservation, _ := budget.Reserve(100)
	child, err := startProcess(t.Context(), processSpec{binary: binary, directory: t.TempDir(), artifact: "fixture"}, nil, device, "fixture", 1)
	if err != nil {
		reservation.Release()
		t.Fatal(err)
	}
	t.Cleanup(child.close)
	if err := child.retain(reservation); err != nil {
		t.Fatal(err)
	}
	return child, device, budget
}

func TestProcessRetainsMemoryAcrossRecoverableErrors(t *testing.T) {
	child, device, budget := fixtureProcess(t)
	err := child.call(context.Background(), command{Method: "capacity"}, "cleared", inference.Foreground, true, nil)
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "capacity" {
		t.Fatalf("capacity response = %v", err)
	}
	if budget.Snapshot().ReservedBytes != 100 || device.Snapshot().Active != nil {
		t.Fatal("recoverable failure released memory or retained a completed step")
	}
	if err := child.call(context.Background(), command{Method: "clear_prefix"}, "cleared", inference.Foreground, true, nil); err != nil {
		t.Fatal(err)
	}
	child.close()
	if budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("reaped process retained its reservation")
	}
}

func TestProcessMalformedResultReapsBeforeReleasingAdmission(t *testing.T) {
	child, device, budget := fixtureProcess(t)
	if err := child.call(context.Background(), command{Method: "invalid"}, "cleared", inference.Foreground, true, nil); err == nil {
		t.Fatal("accepted an undeclared result field")
	}
	select {
	case <-child.done:
	default:
		t.Fatal("call returned before reaping the failed process")
	}
	if device.Snapshot().Active != nil || budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("reaped process retained admission")
	}
}

type notifyingInput struct {
	io.WriteCloser
	wrote chan struct{}
	once  sync.Once
}

func (w *notifyingInput) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	w.once.Do(func() { close(w.wrote) })
	return n, err
}

func TestProcessCancellationReapsBeforeReturning(t *testing.T) {
	for _, method := range []string{"hang", "advance"} {
		t.Run(method, func(t *testing.T) {
			t.Setenv("PW_NATIVEGEN_STEP_HANG", "1")
			assertCanceledProcessReaped(t, method)
		})
	}
}

func assertCanceledProcessReaped(t *testing.T, method string) {
	t.Helper()
	child, device, budget := fixtureProcess(t)
	input := &notifyingInput{WriteCloser: child.input, wrote: make(chan struct{})}
	child.input = input
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- child.call(ctx, command{Method: method}, "cleared", inference.Foreground, true, nil)
	}()
	<-input.wrote
	if device.Snapshot().Active == nil || budget.Snapshot().ReservedBytes != 100 {
		t.Fatal("physical work started without admission")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call = %v", err)
	}
	select {
	case <-child.done:
	default:
		t.Fatal("cancellation returned before process reap")
	}
	if device.Snapshot().Active != nil || budget.Snapshot().ReservedBytes != 0 {
		t.Fatal("cancellation retained admission")
	}
}
