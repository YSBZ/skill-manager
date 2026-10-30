package contentservice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeCloseSpy struct {
	calls  atomic.Int32
	closed chan struct{}
}

func (s *runtimeCloseSpy) Close() error {
	if s.calls.Add(1) == 1 {
		close(s.closed)
	}
	return nil
}

type runtimeBlockingRefresher struct {
	started  chan struct{}
	canceled chan struct{}
}

func (r runtimeBlockingRefresher) Refresh(ctx context.Context, _ time.Time) error {
	close(r.started)
	<-ctx.Done()
	close(r.canceled)
	return ctx.Err()
}

func TestLocalRuntimeCancellationDrainsHandlersBeforeClosingStore(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	store := &runtimeCloseSpy{closed: make(chan struct{})}
	refresh := runtimeBlockingRefresher{started: make(chan struct{}), canceled: make(chan struct{})}
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan struct{})
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-r.Context().Done()
		close(handlerCanceled)
		<-releaseHandler
		w.WriteHeader(http.StatusNoContent)
	})
	runtime := NewLocalRuntime(parent, store, handler, refresh)
	t.Cleanup(func() {
		cancel()
		release()
		_ = runtime.Close()
	})

	select {
	case <-refresh.started:
	case <-time.After(time.Second):
		t.Fatal("initial refresh did not start asynchronously")
	}

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		runtime.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("local handler did not start")
	}

	cancel()
	select {
	case <-refresh.canceled:
	case <-time.After(time.Second):
		t.Fatal("refresh context was not canceled")
	}
	select {
	case <-handlerCanceled:
	case <-time.After(time.Second):
		t.Fatal("in-flight handler context was not canceled")
	}

	select {
	case <-store.closed:
		t.Fatal("store closed before the accepted handler drained")
	default:
	}
	release()
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("in-flight handler did not drain")
	}
	select {
	case <-store.closed:
	case <-time.After(time.Second):
		t.Fatal("store was not closed after shutdown drained")
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("store Close calls=%d, want 1", got)
	}

	rejected := httptest.NewRecorder()
	runtime.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after close status=%d, want 503", rejected.Code)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("repeated Close(): %v", err)
	}
}

func TestLocalRuntimeCloseReturnsStoreError(t *testing.T) {
	want := errors.New("store close failed")
	store := &runtimeErrorCloser{err: want}
	runtime := NewLocalRuntime(context.Background(), store, http.NotFoundHandler())
	if err := runtime.Close(); !errors.Is(err, want) {
		t.Fatalf("Close() error=%v, want %v", err, want)
	}
}

type runtimeErrorCloser struct{ err error }

func (s *runtimeErrorCloser) Close() error { return s.err }
