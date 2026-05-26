package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/smalex-z/gopher/internal/api"
	"github.com/smalex-z/gopher/internal/api/handlers"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/proxy"
	"github.com/smalex-z/gopher/internal/service"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install", "upgrade":
			// `upgrade` is a clarity alias for `install`. install is fully
			// idempotent (users / sudoers / systemd unit / data dir are all
			// no-ops on a re-run) and the binary swap is atomic-rename, so
			// running it on top of a live install hot-swaps the binary +
			// restarts the service without touching gopher.db. That's the
			// supported path for "I downloaded a new release, apply it
			// without redoing the setup wizard."
			if err := runInstall(os.Args[2:]); err != nil {
				log.Fatalf("%s failed: %v", os.Args[1], err)
			}
			return
		case "uninstall":
			if err := runUninstall(os.Args[2:]); err != nil {
				log.Fatalf("Uninstall failed: %v", err)
			}
			return
		}
	}

	runServer(os.Args[1:])
}

func runServer(args []string) {
	flags := flag.NewFlagSet("gopher", flag.ExitOnError)
	port := flags.String("port", "4321", "server port")
	dbPath := flags.String("db", "./gopher.db", "database path")
	// --dev runs gopher against an isolated DB without touching any
	// system-managed file: /etc/rathole/server.toml, /etc/caddy/conf.d/*,
	// /etc/sudoers.d/gopher, ~/.ssh/authorized_keys. Used by scripts/dev.sh
	// for frontend work on a host that already has a production install.
	// Without this, a dev process running from a repo with a stale
	// ./gopher.db will reconcile production's rathole config to the dev
	// DB's contents on startup and silently kill every live tunnel.
	devMode := flags.Bool("dev", false, "developer mode: skip every system-state write (rathole config, Caddy, sudoers, authorized_keys)")
	_ = flags.Parse(args)

	if *devMode {
		service.SetDevMode(true)
		log.Printf("dev mode active: system writes (rathole config, Caddy, sudoers, authorized_keys) are no-ops")
	} else {
		if err := ensurePasswordlessSudoForCurrentUser(); err != nil {
			log.Printf("Warning: could not configure passwordless sudo automatically: %v", err)
		}
	}

	if p, err := strconv.Atoi(*port); err == nil {
		service.SetDashboardPort(p)
	}

	if err := db.Initialize(*dbPath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	deploySvc := service.NewDeployService()
	localSvc := service.NewLocalSetupService(deploySvc.Hub)
	vpsSvc := service.NewVPSService(deploySvc)
	machineSvc := service.NewMachineService(deploySvc, localSvc)
	tunnelSvc := service.NewTunnelService(localSvc)
	authSvc := service.NewAuthService()
	bootstrapSvc := service.NewBootstrapService(localSvc)
	updateSvc := service.NewUpdateService()
	secSvc := service.NewSecurityService()
	backupSvc := service.NewBackupService(*dbPath)
	agentInstaller := service.NewAgentInstaller(localSvc)
	healthSvc := service.NewHealthService(true)
	// Wire the deferred-push retry hook before Start: when a previously-
	// unreachable machine comes back online, the health loop retries the
	// client.toml push that the migration couldn't land. Setter-style to
	// avoid a circular dep with LocalSetupService.
	healthSvc.SetConfigPusher(localSvc)
	healthSvc.Start()
	go secSvc.SyncFail2banConfig()
	monitorSvc := service.NewMonitorService()
	monitorSvc.Start()
	if !*devMode {
		// Drop conf.d/gopher-tunnel-*.caddy orphans BEFORE the first Caddy
		// reload — otherwise ReconcileMainCaddyfile's reload still sees the
		// stale files and fails with "ambiguous site definition" when two
		// orphan files claim the same subdomain.
		localSvc.ReconcileTunnelCaddyFiles()
		// Regenerate every tunnel's Caddy file from DB. Self-heals the case
		// where a previous deploy or manual edit removed a live tunnel's
		// Caddy file — without this, the dashboard says "tunnel online" but
		// the subdomain returns SSL errors because Caddy has no upstream
		// route. Idempotent: matches existing content do nothing.
		if err := localSvc.ReconcileAllTunnelCaddyBlocks(); err != nil {
			log.Printf("startup: reconcile tunnel caddy blocks: %v", err)
		}
		localSvc.ReconcileMainCaddyfile()
		localSvc.ReconcileRouterCaddyBlock()
		// Self-heal upgraded installs: when a binary is swapped without re-running
		// `gopher install` / scripts/reinstall.sh, the gopher-jump system user may
		// not exist yet on legacy boxes — without it ReconcileAuthorizedKeys falls
		// back to the dashboard user (the OLD insecure layout). EnsureJumpboxUser
		// creates it via sudo useradd; the next reconcile picks it up.
		localSvc.EnsureJumpboxUser()
		localSvc.ReconcileAuthorizedKeys()
		// Re-derive /etc/rathole/server.toml from the DB on every boot. Catches
		// drift introduced by DB restore from backup, partial writes, or a crash
		// between a tunnel/machine row delete and the disk reconcile that would
		// otherwise have followed it. Idempotent — no-op when on-disk already
		// matches the DB.
		if err := localSvc.ReconcileServerConfig(); err != nil {
			log.Printf("startup: failed to reconcile rathole server config: %v", err)
		}
		// One-shot upgrade from plaintext rathole transport → encrypted noise.
		// Runs in a goroutine so a slow SSH push to one offline machine doesn't
		// hold up the dashboard coming online. No-op on installs that have
		// already migrated or haven't completed the wizard yet.
		go func() {
			if err := localSvc.MigrateRatholeNoise(); err != nil {
				log.Printf("startup: rathole noise migration: %v", err)
			}
		}()
	} else {
		log.Printf("dev mode: skipping rathole/Caddy/sudoers/authorized_keys reconciles")
	}

	// Bot-protection middleware — runs inside the existing server, no extra port.
	botMiddleware, botErr := proxy.NewMiddleware()
	if botErr != nil {
		log.Fatalf("Failed to create bot-protection middleware: %v", botErr)
	}

	// Hourly housekeeping: drop expired bot sessions, bootstrap tokens, and
	// migration tokens. None of these are load-bearing once expired, but
	// without a sweep the tables grow forever. Stops on shutdown so the
	// goroutine doesn't get killed mid-DELETE.
	purgeStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-purgeStop:
				return
			case <-ticker.C:
				if err := db.PurgeBotSessions(); err != nil {
					log.Printf("bot session purge: %v", err)
				}
				if _, err := db.PurgeExpiredBootstrapTokens(); err != nil {
					log.Printf("bootstrap token purge: %v", err)
				}
				if _, err := db.PurgeExpiredMigrationTokens(); err != nil {
					log.Printf("migration token purge: %v", err)
				}
			}
		}
	}()

	router := api.NewRouter(vpsSvc, machineSvc, tunnelSvc, deploySvc, bootstrapSvc, authSvc, localSvc, updateSvc, secSvc, backupSvc, agentInstaller, healthSvc)

	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	// /static/agents/* serves the gopher-agent binaries; everything else under
	// /static/ goes through the chi router (bootstrap.sh, etc.). ServeMux uses
	// longest-prefix match, so the agents handler wins for that subtree.
	mux.Handle("/static/agents/", http.StripPrefix("/static/agents/", agentsHandler()))
	mux.Handle("/static/", router)
	// Top-level liveness probe. Lives outside the chi /api group so external
	// monitors can hit a stable canonical path without auth. ServeMux's
	// "/healthz" pattern matches the exact path and beats the catch-all "/"
	// SPA handler below.
	mux.HandleFunc("/healthz", handlers.HealthzHandler)

	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Printf("Warning: could not set up frontend serving: %v", err)
	} else {
		mux.Handle("/", spaHandler(http.FS(distFS)))
	}

	listenAddr := ":" + *port
	if settings, sErr := db.GetSettings(); sErr == nil && settings.BindIP != "" {
		// Restrict dashboard to 127.0.0.1 — not reachable on other interfaces.
		// Caddy still proxies to localhost:port so this is transparent to users.
		listenAddr = "127.0.0.1:" + *port
	}

	// Configure the HTTP server with explicit timeouts. ReadHeaderTimeout
	// defends against slow-loris connections without affecting WebSocket
	// upgrades (the deadline only applies to the initial header read).
	// We deliberately do NOT set ReadTimeout / WriteTimeout — those would
	// tear down the long-lived WebSocket connections that stream install /
	// firewall logs to the dashboard. IdleTimeout is safe to bound; closes
	// keep-alive connections that go idle.
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           botMiddleware.Wrap(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Run the server in a goroutine so the main thread can wait on signals
	// and trigger a graceful drain. Without this, SIGTERM (sent by
	// `systemctl stop gopher` and the upgrade flow) would kill in-flight
	// HTTP requests, install/firewall goroutines mid-step, and any DB
	// writes mid-transaction.
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Server starting on %s", listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("Server failed: %v", err)
	case sig := <-sigCh:
		log.Printf("received %s, draining...", sig)
	}

	// Give in-flight HTTP requests up to 25s to drain (systemd's default
	// SIGTERM grace is 90s, so we have headroom). After that, in-flight
	// requests are dropped and the listener closes.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}

	// Stop background services so their goroutines exit before the process
	// does. Each Stop() is idempotent and bounded — they won't block past
	// their own internal timeout even if a worker is wedged.
	close(purgeStop)
	monitorSvc.Stop()
	healthSvc.Stop()
	log.Printf("shutdown complete")
}

// spaHandler serves static files and falls back to index.html for unknown paths,
// enabling client-side routing in the React SPA.
func spaHandler(fsys http.FileSystem) http.Handler {
	fileServer := http.FileServer(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := fsys.Open(r.URL.Path)
		if err != nil {
			// File not found — serve index.html so the SPA router handles it
			r2 := *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
}
