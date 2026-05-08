package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/smalex-z/gopher/internal/api"
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
	if err := ensurePasswordlessSudoForCurrentUser(); err != nil {
		log.Printf("Warning: could not configure passwordless sudo automatically: %v", err)
	}

	flags := flag.NewFlagSet("gopher", flag.ExitOnError)
	port := flags.String("port", "4321", "server port")
	dbPath := flags.String("db", "./gopher.db", "database path")
	_ = flags.Parse(args)

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
	healthSvc.Start()
	go secSvc.SyncFail2banConfig()
	monitorSvc := service.NewMonitorService()
	monitorSvc.Start()
	localSvc.ReconcileMainCaddyfile()
	localSvc.ReconcileRouterCaddyBlock()
	localSvc.ReconcileAuthorizedKeys()

	// Bot-protection middleware — runs inside the existing server, no extra port.
	botMiddleware, botErr := proxy.NewMiddleware()
	if botErr != nil {
		log.Fatalf("Failed to create bot-protection middleware: %v", botErr)
	}

	// Purge expired bot sessions hourly.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.PurgeBotSessions(); err != nil {
				log.Printf("bot session purge: %v", err)
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
	log.Printf("Server starting on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, botMiddleware.Wrap(mux)); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
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
