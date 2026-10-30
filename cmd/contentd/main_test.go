package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"skillmanage/internal/content/sqlite"
)

func TestLoadConfigRequiresDatabasePath(t *testing.T) {
	t.Setenv("CONTENT_DB_PATH", "")
	t.Setenv("CONTENT_LISTEN_ADDR", "")

	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig() succeeded without CONTENT_DB_PATH")
	}
}

func TestLoadConfigDefaultsToLoopback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "content.db")
	t.Setenv("CONTENT_DB_PATH", dbPath)
	t.Setenv("CONTENT_LISTEN_ADDR", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig(): %v", err)
	}
	if cfg.DBPath != dbPath {
		t.Fatalf("DBPath=%q, want %q", cfg.DBPath, dbPath)
	}
	if cfg.ListenAddr != "127.0.0.1:8088" {
		t.Fatalf("ListenAddr=%q, want 127.0.0.1:8088", cfg.ListenAddr)
	}
}

func TestLoadConfigRejectsNonLoopbackBind(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:8088",
		"192.0.2.10:8088",
		":8088",
		"localhost:8088",
		"[::]:8088",
	} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("CONTENT_DB_PATH", filepath.Join(t.TempDir(), "content.db"))
			t.Setenv("CONTENT_LISTEN_ADDR", addr)

			if _, err := LoadConfig(); err == nil {
				t.Fatalf("LoadConfig() accepted non-loopback address %q", addr)
			}
		})
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	server := newHTTPServer(config{ListenAddr: "127.0.0.1:8088"}, http.NotFoundHandler())
	if server.ReadHeaderTimeout.String() != "5s" || server.ReadTimeout.String() != "15s" ||
		server.WriteTimeout.String() != "30s" || server.IdleTimeout.String() != "1m0s" {
		t.Fatalf("server timeouts = header %s, read %s, write %s, idle %s",
			server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestAppHealthzWithSQLite(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "content.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close(): %v", err)
		}
	})

	app := newApp(store, nil, nil)
	w := httptest.NewRecorder()
	app.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = status %d, body %q", w.Code, w.Body.String())
	}
}

func TestNewAppIncludesGitHubRefresher(t *testing.T) {
	service := newApp(nil, nil, nil)
	if service.githubRefresher == nil {
		t.Fatal("newApp returned a nil GitHub refresher")
	}
}

func TestShutdownHTTPServerDrainsHandlersBeforeStoreClose(t *testing.T) {
	handlerStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerCompleted := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }

	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-r.Context().Done()
		close(requestCanceled)
		<-releaseHandler
		close(handlerCompleted)
	})
	tracker := newHandlerTracker(handler)
	server := httptest.NewServer(tracker)
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatalf("sqlite.Open(): %v", err)
	}
	storeClosed := make(chan struct{})
	var storeCloseOnce sync.Once
	closeStore := func() error {
		var closeErr error
		storeCloseOnce.Do(func() {
			closeErr = store.Close()
			close(storeClosed)
		})
		return closeErr
	}
	defer func() {
		release()
		server.Close()
		_ = closeStore()
	}()

	shutdownStarted := make(chan struct{})
	server.Config.RegisterOnShutdown(func() { close(shutdownStarted) })
	requestDone := make(chan struct{})
	go func() {
		response, _ := http.Get(server.URL)
		if response != nil {
			_ = response.Body.Close()
		}
		close(requestDone)
	}()

	waitFor := func(label string, ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}
	waitFor("handler start", handlerStarted)

	schedulerDone := make(chan struct{})
	close(schedulerDone)
	type shutdownOutcome struct {
		err                         error
		storeCloseErr               error
		handlerCompletedBeforeClose bool
	}
	shutdownDone := make(chan shutdownOutcome, 1)
	go func() {
		shutdownErr := shutdownHTTPServer(server.Config, tracker, 20*time.Millisecond)
		<-schedulerDone
		completedBeforeClose := false
		select {
		case <-handlerCompleted:
			completedBeforeClose = true
		default:
		}
		closeErr := closeStore()
		shutdownDone <- shutdownOutcome{err: shutdownErr, storeCloseErr: closeErr, handlerCompletedBeforeClose: completedBeforeClose}
	}()

	waitFor("HTTP shutdown start", shutdownStarted)
	rejected := httptest.NewRecorder()
	tracker.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after shutdown admission closed: status %d, want %d", rejected.Code, http.StatusServiceUnavailable)
	}
	waitFor("forced request cancellation", requestCanceled)
	waitFor("handler drain", tracker.drainStarted)
	select {
	case <-storeClosed:
		t.Fatal("store closed while an admitted handler was still running")
	default:
	}

	release()
	waitFor("handler completion", handlerCompleted)
	waitFor("store close", storeClosed)
	waitFor("client request completion", requestDone)

	outcome := <-shutdownDone
	if !errors.Is(outcome.err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want grace deadline", outcome.err)
	}
	if !outcome.handlerCompletedBeforeClose {
		t.Fatal("store close ran before the handler completed")
	}
	if outcome.storeCloseErr != nil {
		t.Fatalf("store close: %v", outcome.storeCloseErr)
	}
}
