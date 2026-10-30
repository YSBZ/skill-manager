package contentservice

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testRefresher struct {
	called chan struct{}
	err    error
	onCall func()
}

func (r testRefresher) Refresh(context.Context, time.Time) error {
	if r.onCall != nil {
		r.onCall()
	}
	r.called <- struct{}{}
	return r.err
}

func TestRunSchedulerContinuesAfterSourceFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := testRefresher{called: make(chan struct{}, 1), err: errors.New("offline")}
	second := testRefresher{called: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		RunScheduler(ctx, time.Hour, first, second)
		close(done)
	}()
	for i, ch := range []chan struct{}{first.called, second.called} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("refresher %d not called", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestRunSchedulerStopsBeforeLaterSourcesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := testRefresher{called: make(chan struct{}, 1), onCall: cancel}
	second := testRefresher{called: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		RunScheduler(ctx, time.Hour, first, second)
		close(done)
	}()
	select {
	case <-first.called:
	case <-time.After(time.Second):
		t.Fatal("first refresher not called")
	}
	select {
	case <-second.called:
		t.Fatal("later refresher called after cancellation")
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}
