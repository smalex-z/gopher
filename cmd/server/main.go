package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/smalex-z/gopher/internal/api"
	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/service"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			if err := runInstall(os.Args[2:]); err != nil {
				log.Fatalf("Install failed: %v", err)
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
	port := flags.String("port", "8080", "server port")
	dbPath := flags.String("db", "./gopher.db", "database path")
	_ = flags.Parse(args)

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
	monitorSvc := service.NewMonitorService()
	monitorSvc.Start()

	router := api.NewRouter(vpsSvc, machineSvc, tunnelSvc, deploySvc, bootstrapSvc, authSvc, localSvc)

	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/static/", router)

	distFS, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Printf("Warning: could not set up frontend serving: %v", err)
	} else {
		mux.Handle("/", spaHandler(http.FS(distFS)))
	}

	log.Printf("Server starting on :%s", *port)
	if err := http.ListenAndServe(":"+*port, mux); err != nil {
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
