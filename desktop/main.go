// Command desktop is a Wails PoC: it wraps the existing SkillManage daemon in a
// native desktop window (WKWebView on macOS) instead of a browser tab. The
// window's asset server IS the daemon's own http.Handler — same embedded UI,
// same API, in-process, no localhost port or iframe. This is a proof of concept
// to evaluate the desktop experience and bundle size; it is not yet wired into
// the daemon's single-instance / takeover lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"skillmanage/internal/askpass"
	"skillmanage/internal/cli"
	"skillmanage/internal/config"
	"skillmanage/internal/daemon"
	"skillmanage/internal/lock"
	"skillmanage/internal/pathenv"
	"skillmanage/internal/server"
	"skillmanage/internal/toolchain"
)

// loopbackHost normalizes the request Host/Origin to loopback before handing off
// to the daemon's handler. The Wails webview serves assets from "wails.localhost",
// which the daemon's anti-DNS-rebinding Host check (and the npx endpoints' Origin
// check) reject. These requests originate from the in-process webview, not the
// network, so forcing loopback is safe here.
func loopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1"
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", "http://127.0.0.1")
		}
		// The webview serves the page from wails.localhost, so requests carry a
		// Referer of https://wails.localhost/… . The daemon's origin guard falls
		// back to Referer when Origin is absent (it is for some GETs), and would
		// reject wails.localhost as cross-origin. Normalize it to loopback too.
		if r.Header.Get("Referer") != "" {
			r.Header.Set("Referer", "http://127.0.0.1/")
		}
		next.ServeHTTP(w, r)
	})
}

// version is the SkillManager release, injected at build time via
// -ldflags "-X main.version=<v>" (single source: build/macos/Info.plist). Shown
// in the window title and the in-app header. Defaults to "dev".
var version = "dev"

// appCtx holds the Wails runtime context, captured in OnStartup. The focus
// handler (raise-window-to-front) reads it to call the runtime; it's set once at
// startup and read later from an HTTP goroutine, so it's mutex-guarded.
var (
	appCtxMu sync.Mutex
	appCtx   context.Context
)

func setAppCtx(ctx context.Context) { appCtxMu.Lock(); appCtx = ctx; appCtxMu.Unlock() }
func getAppCtx() context.Context    { appCtxMu.Lock(); defer appCtxMu.Unlock(); return appCtx }

// cliAPIPort is the preferred loopback port for the CLI-facing API listener
// (same value the standalone daemon uses). srv.Bind falls back to an OS-assigned
// port if it's taken, and the CLI reads the resolved address from the address file.
const cliAPIPort = 7799

// updateFeed / updateToken are the update feed URL and optional read-only token,
// injected at build time. Public release builds configure the public feed.
var (
	updateFeed  = ""
	updateToken = ""
	releaseCode = ""

	feedbackEndpoint = ""
	feedbackToken    = ""
)

func main() {
	// Credential-helper mode FIRST: the daemon wires the running executable as
	// git's GIT_ASKPASS, and for the desktop build that executable is THIS Wails
	// binary. git invokes it with the credential prompt — answer and exit before
	// any window/lifecycle init, exactly like the CLI binary. Without this, the
	// desktop app would try to boot a window when git asks for credentials, so
	// HTTPS auth (private-repo fetch AND the new contribute push) silently fails.
	if askpass.Active() {
		askpass.Run()
		return
	}

	// Headless CLI mode: a bare-word first arg drives the ALREADY-RUNNING app over
	// its loopback API (enable/disable/move/adopt/sync/list) instead of opening a
	// window. Dash-prefixed args (Finder's -psn_…) stay on the window path; an
	// unknown word self-reports via Run's usage error (never a silent launch).
	if len(os.Args) > 1 && cli.IsInvocation(os.Args[1]) {
		os.Exit(cli.Run(os.Args[1:]))
	}

	// Same PATH hardening as the daemon, so npx/git work when launched as an app.
	pathenv.Ensure()

	dir, err := config.DefaultCentralDir()
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}

	// GUI launches have no console — log startup + any panic to a file so a
	// crash-on-launch (which looks like the window flickering open/closed) is
	// diagnosable on another machine.
	logf, _ := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		log.SetOutput(logf)
		defer logf.Close()
	}
	log.Printf("desktop: starting (pid=%d, exe=%s)", os.Getpid(), mustExe())
	defer func() {
		if r := recover(); r != nil {
			log.Printf("desktop: PANIC: %v\n%s", r, debug.Stack())
			logf.Close()
			os.Exit(1)
		}
	}()

	// Single-instance guard. Takeover replaces a headless CLI daemon, but it must
	// NOT kill a live DESKTOP window: if it did, any external relaunch (Dock,
	// login item, a stray launcher) would become a kill→reopen→kill flicker loop
	// (observed in the wild). So when a *live desktop* instance already owns the
	// lock, this launch simply bows out; takeover is reserved for a CLI-daemon
	// owner or a stale (dead-PID) lock.
	lockPath := config.LockfilePath(dir)
	lk, err := lock.Acquire(lockPath)
	if errors.Is(err, lock.ErrLocked) {
		holder := daemon.ReadPid(dir)
		kind := daemon.ReadKind(dir)
		alive := daemon.Alive(holder)
		// Log the decision so a silent "double-click does nothing" is diagnosable
		// from desktop.log (a GUI app has no console, so stderr alone is invisible).
		log.Printf("desktop: lock 被占用 (holder pid=%d, kind=%q, alive=%v)，尝试接管", holder, kind, alive)
		if holder > 0 && holder != os.Getpid() && alive && kind == "desktop" {
			// Don't kill the live window (that caused a kill→reopen flicker loop);
			// instead ask it to come to the foreground, then exit. Without this the
			// new GUI process just dies — a Dock-icon flash with no window, which
			// the user reads as a crash ("闪退"). Best-effort: if the running
			// instance has no API listener, we simply exit as before.
			if daemon.RequestFocus(dir) {
				log.Printf("desktop: 已有桌面实例在运行 (pid=%d)，已请求其前置窗口后退出", holder)
			} else {
				log.Printf("desktop: 已有桌面实例在运行 (pid=%d)，前置请求未成功，本次启动退出", holder)
			}
			os.Exit(0)
		}
		if lk, err = daemon.TakeOver(dir, lockPath); err != nil {
			log.Printf("desktop: 接管失败：另一个实例 (pid=%d, kind=%q) 仍占用锁且无法停止 —— %v。"+
				"请在任务管理器结束所有 skillmanager.exe / SkillManager.exe 后重试。", holder, kind, err)
			fmt.Fprintln(os.Stderr, "desktop: 已有一个实例在运行且无法接管")
			os.Exit(1)
		}
		log.Printf("desktop: 接管成功 (原 pid=%d)", holder)
	} else if err != nil {
		log.Printf("desktop: 获取锁出错：%v", err)
		panic(err)
	}
	defer lk.Release()
	daemon.WritePid(dir)
	defer daemon.RemovePid(dir)
	daemon.WriteKind(dir, "desktop")
	defer daemon.RemoveKind(dir)

	if err := toolchain.ActivateDefault(dir); err != nil {
		panic(fmt.Errorf("prepare bundled tools: %w", err))
	}
	log.Printf("desktop: lock acquired, starting server")
	server.Version = version
	server.ReleaseCode = releaseCode
	server.UpdateFeed, server.UpdateToken = updateFeed, updateToken
	server.FeedbackEndpoint, server.FeedbackToken = feedbackEndpoint, feedbackToken
	server.IsDesktop = true // self-update targets the desktop app
	srv, err := server.New(dir)
	if err != nil {
		panic(err)
	}
	defer srv.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	srv.SetBaseContext(ctx)
	if err := srv.StartLocalContent(ctx); err != nil {
		log.Printf("desktop: content startup failed: %v", err)
	}
	// Raise-window handler for POST /api/focus: a second launch calls it (via
	// daemon.RequestFocus) so relaunching a running app surfaces THIS window
	// instead of the new process flashing and vanishing. Uses the Wails ctx
	// captured in OnStartup; a no-op until the window exists.
	srv.SetFocusHandler(func() {
		if c := getAppCtx(); c != nil {
			wruntime.WindowUnminimise(c)
			wruntime.Show(c)
			wruntime.WindowShow(c)
		}
	})
	srv.ReconcileOnly()
	go srv.RunScheduler(ctx)

	// Loopback API listener (127.0.0.1 only), same handler the webview uses, so the
	// `skillmanager` CLI — and cc/codex hooks / scripts — can drive the SAME live
	// daemon over HTTP (enable/disable/move/adopt), keeping config+manifest
	// consistent with zero GUI. Every /api/* route already requires the Bearer token
	// (config.TokenPath, 0600) + a loopback Host, so this adds no new attack surface
	// beyond the standalone daemon (which already binds this port). Best-effort: a
	// bind failure disables the CLI for this run but must not block the window.
	if ln, berr := srv.Bind(cliAPIPort); berr != nil {
		log.Printf("desktop: CLI listener bind failed (CLI disabled this run): %v", berr)
	} else {
		if werr := srv.WriteAddress(ln.Addr().String()); werr != nil {
			log.Printf("desktop: write address failed: %v", werr)
		}
		apiSrv := &http.Server{Handler: srv.Handler()}
		go func() { _ = apiSrv.Serve(ln) }()
		go func() {
			<-ctx.Done()
			sc, c := context.WithTimeout(context.Background(), 3*time.Second)
			defer c()
			_ = apiSrv.Shutdown(sc)
		}()
		log.Printf("desktop: CLI API listening at %s", ln.Addr().String())
	}

	log.Printf("desktop: opening window")
	err = wails.Run(&options.App{
		Title:  "SkillManager v" + version,
		Width:  1180,
		Height: 820,
		// Capture the runtime context so POST /api/focus can raise this window
		// when a second launch bows out (single-instance foreground behavior).
		OnStartup: func(startupCtx context.Context) { setAppCtx(startupCtx) },
		// The window loads the daemon's UI through its own handler — no browser,
		// no port. Relative /api/* fetches resolve back to this same handler.
		AssetServer: &assetserver.Options{Handler: loopbackHost(srv.Handler())},
		// Standard title bar: a normal draggable bar with the traffic lights in it,
		// so they don't overlap the app's own header. (HiddenInset looked nicer but
		// left no drag region and collided with the logo.)
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
		},
	})
	// Window closed / app quit → cancel the scheduler and drain in-flight syncs
	// before the deferred srv.Close()/lk.Release() tear things down.
	stop()
	srv.WaitForSyncs()
	if err != nil {
		log.Printf("desktop: wails.Run error: %v", err)
		fmt.Fprintln(os.Stderr, "desktop:", err)
	}
	log.Printf("desktop: clean exit")
}

func mustExe() string { e, _ := os.Executable(); return e }
