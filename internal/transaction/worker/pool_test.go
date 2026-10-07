package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

var errTransient = errors.New("временный сбой")

func TestRetriesOnlyWhenAllowed(t *testing.T) {
	p := NewWorkerPool(2, 10, 3)
	p.Start()
	defer p.Shutdown(time.Second)

	run := func(retryOn func(error) bool) (calls int32, err error) {
		var n atomic.Int32
		done := make(chan error, 1)
		if err := p.Submit(Job{
			ID:      "job",
			Task:    func(context.Context) error { n.Add(1); return errTransient },
			RetryOn: retryOn,
			OnDone:  func(err error) { done <- err },
		}); err != nil {
			t.Fatal(err)
		}
		err = <-done
		return n.Load(), err
	}

	if calls, err := run(nil); calls != 1 || !errors.Is(err, errTransient) {
		t.Fatalf("без RetryOn задача не должна повторяться: вызовов %d, ошибка %v", calls, err)
	}
	if calls, err := run(func(error) bool { return true }); calls != 4 || !errors.Is(err, errTransient) {
		t.Fatalf("ожидалось 1 + 3 повтора, вызовов %d, ошибка %v", calls, err)
	}
}

func TestSubmitAfterShutdownDoesNotPanic(t *testing.T) {
	p := NewWorkerPool(1, 1, 0)
	p.Start()
	if err := p.Shutdown(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(Job{ID: "late", Task: func(context.Context) error { return nil }}); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("ожидался ErrPoolClosed, получено %v", err)
	}
}

func TestQueueFull(t *testing.T) {
	p := NewWorkerPool(1, 1, 0) // воркеры не запущены — очередь не разбирается
	job := Job{ID: "j", Task: func(context.Context) error { return nil }}
	if err := p.Submit(job); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(job); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("ожидался ErrQueueFull, получено %v", err)
	}
}
