package main

import (
"embed"
"flag"
"io/fs"
"log"
"net/http"

"github.com/smalex-z/gopher/internal/api"
"github.com/smalex-z/gopher/internal/db"
"github.com/smalex-z/gopher/internal/service"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

func main() {
port := flag.String("port", "8080", "server port")
dbPath := flag.String("db", "./gopher.db", "database path")
flag.Parse()

if err := db.Initialize(*dbPath); err != nil {
log.Fatalf("Failed to initialize database: %v", err)
}

deploySvc := service.NewDeployService()
vpsSvc := service.NewVPSService(deploySvc)
machineSvc := service.NewMachineService(deploySvc)
tunnelSvc := service.NewTunnelService()
monitorSvc := service.NewMonitorService()
monitorSvc.Start()

router := api.NewRouter(vpsSvc, machineSvc, tunnelSvc, deploySvc)

mux := http.NewServeMux()
mux.Handle("/api/", router)

distFS, err := fs.Sub(frontendDist, "frontend/dist")
if err != nil {
log.Printf("Warning: could not set up frontend serving: %v", err)
} else {
mux.Handle("/", http.FileServer(http.FS(distFS)))
}

log.Printf("Server starting on :%s", *port)
if err := http.ListenAndServe(":"+*port, mux); err != nil {
log.Fatalf("Server failed: %v", err)
}
}
