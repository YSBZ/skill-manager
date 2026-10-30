package contentservice

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"skillmanage/internal/content"
)

const LocalRefreshInterval = 10 * time.Minute

// LocalRuntime owns the refresh scheduler, the handler admission window, and
// the store used by an in-process content service.
type LocalRuntime struct {
	handler http.Handler
	store   io.Closer
	ctx     context.Context
	cancel  context.CancelFunc

	schedulerDone chan struct{}

	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

// NewLocalRuntime starts the initial refresh asynchronously and then refreshes
// on the shared ten-minute interval. Close stops admission, cancels refreshes
// and accepted requests, drains both before closing store.
func NewLocalRuntime(parent context.Context, store io.Closer, handler http.Handler, refreshers ...Refresher) *LocalRuntime {
	if parent == nil {
		parent = context.Background()
	}
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	ctx, cancel := context.WithCancel(parent)
	runtime := &LocalRuntime{
		handler:       handler,
		store:         store,
		ctx:           ctx,
		cancel:        cancel,
		schedulerDone: make(chan struct{}),
		accepting:     true,
		drained:       make(chan struct{}),
		closed:        make(chan struct{}),
	}
	go func() {
		defer close(runtime.schedulerDone)
		RunScheduler(ctx, LocalRefreshInterval, refreshers...)
	}()
	go func() {
		<-ctx.Done()
		_ = runtime.Close()
	}()
	return runtime
}

// ServeHTTP forwards a request to the local content handler while tracking it
// so store closure cannot race an accepted catalog read.
func (r *LocalRuntime) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	if !r.accepting || r.ctx.Err() != nil {
		r.mu.Unlock()
		writeAPIError(w, content.ErrUnavailable)
		return
	}
	r.active++
	r.mu.Unlock()
	defer r.complete()

	ctx, cancel := context.WithCancel(req.Context())
	stopRuntimeCancel := context.AfterFunc(r.ctx, cancel)
	defer func() {
		stopRuntimeCancel()
		cancel()
	}()
	r.handler.ServeHTTP(w, req.Clone(ctx))
}

func (r *LocalRuntime) complete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	if !r.accepting && r.active == 0 {
		close(r.drained)
	}
}

// Close is safe to call more than once, including concurrently with parent
// context cancellation.
func (r *LocalRuntime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.accepting = false
		if r.active == 0 {
			close(r.drained)
		}
		r.mu.Unlock()

		r.cancel()
		<-r.schedulerDone
		<-r.drained
		if r.store != nil {
			r.closeErr = r.store.Close()
		}
		close(r.closed)
	})
	<-r.closed
	return r.closeErr
}
