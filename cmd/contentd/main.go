package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"skillmanage/internal/content"
	"skillmanage/internal/content/aihot"
	"skillmanage/internal/content/sqlite"
	"skillmanage/internal/contentgithub"
	"skillmanage/internal/contentservice"
)

const aihotBaseURL = "https://aihot.news"
const githubBaseURL = "https://api.github.com"

type app struct {
	handler         http.Handler
	aihotRefresher  *aihot.Refresher
	githubRefresher *contentgithub.Collector
}

func main() {
	if err := run(); err != nil {
		// Do not print configuration, database, or upstream details to service logs.
		log.Print("contentd stopped after an error")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return errors.New("open content database")
	}

	httpClient := &http.Client{}
	service := newApp(
		store,
		aihot.NewClient(aihotBaseURL, httpClient),
		contentgithub.NewClient(githubBaseURL, httpClient, os.Getenv("GITHUB_TOKEN")),
	)
	handlers := newHandlerTracker(service.handler)
	server := newHTTPServer(cfg, handlers)

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	schedulerCtx, stopScheduler := context.WithCancel(context.Background())
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		contentservice.RunScheduler(schedulerCtx, 10*time.Minute, service.aihotRefresher, service.githubRefresher)
	}()

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	var runErr error
	select {
	case <-signalCtx.Done():
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = errors.New("serve content HTTP server")
		}
	}

	// Cancel and join the scheduler before closing the store so no refresh can
	// write to SQLite after it has been closed. HTTP handlers are also drained
	// before the store closes, including after a forced connection close.
	stopScheduler()
	shutdownErr := shutdownHTTPServer(server, handlers, 10*time.Second)
	<-schedulerDone

	if err := store.Close(); err != nil {
		return errors.Join(runErr, errors.New("close content database"), shutdownErr)
	}
	if shutdownErr != nil {
		return errors.Join(runErr, errors.New("shut down content HTTP server"))
	}
	return runErr
}

func newApp(store content.CatalogStore, client *aihot.Client, githubClient *contentgithub.Client) app {
	aihotRefresher := aihot.NewRefresher(client, store)
	githubRefresher := contentgithub.NewCollector(store, githubClient, contentgithub.DefaultConfig())
	return app{
		handler:         contentservice.NewCatalogHandler(store, aihotRefresher.ResolveDetail),
		aihotRefresher:  aihotRefresher,
		githubRefresher: githubRefresher,
	}
}

func newHTTPServer(cfg config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

type handlerTracker struct {
	handler http.Handler

	mu           sync.Mutex
	accepting    bool
	active       int
	drained      chan struct{}
	drainStarted chan struct{}
	drainOnce    sync.Once
}

func newHandlerTracker(handler http.Handler) *handlerTracker {
	return &handlerTracker{
		handler:      handler,
		accepting:    true,
		drained:      make(chan struct{}),
		drainStarted: make(chan struct{}),
	}
}

func (t *handlerTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	if !t.accepting {
		t.mu.Unlock()
		http.Error(w, "service shutting down", http.StatusServiceUnavailable)
		return
	}
	t.active++
	t.mu.Unlock()

	defer t.complete()
	t.handler.ServeHTTP(w, r)
}

func (t *handlerTracker) stopAdmission() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.accepting {
		return
	}
	t.accepting = false
	if t.active == 0 {
		close(t.drained)
	}
}

func (t *handlerTracker) complete() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active--
	if !t.accepting && t.active == 0 {
		close(t.drained)
	}
}

func (t *handlerTracker) wait() {
	t.drainOnce.Do(func() { close(t.drainStarted) })
	<-t.drained
}

func shutdownHTTPServer(server *http.Server, handlers *handlerTracker, grace time.Duration) error {
	handlers.stopAdmission()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), grace)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancelShutdown()
	if shutdownErr != nil {
		_ = server.Close()
	}
	handlers.wait()
	return shutdownErr
}
